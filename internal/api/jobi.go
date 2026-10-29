package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

// Sign in with JOBI: FDC's employee single sign-on, provided by VCS
// (authorization code + PKCE). JOBI only admits active employees, so accounts
// created here get the recruiter role. They are keyed by JOBI's stable "sub",
// never by email, and have no password in this app.

const (
	jobiFlowCookie = "ais_jobi_flow"
	jobiFlowPath   = "/api/v1/auth/jobi"
)

type jobiFlow struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
}

type jobiProfile struct {
	Sub            string `json:"sub"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	EmailVerified  bool   `json:"email_verified"`
	RoleCode       string `json:"role_code"`
	DepartmentName string `json:"department_name"`
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeNext keeps redirects on this site.
func safeNext(p string) string {
	if strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "/\\") && len(p) <= 300 {
		return p
	}
	return ""
}

// publicHost is the host the visitor is on (our own Next.js server forwards it).
func (a *API) publicHost(r *http.Request) string {
	if a.cfg.SameHostOrigin {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			return h
		}
	}
	return r.Host
}

func (a *API) jobiRedirectURI(r *http.Request) string {
	return "https://" + a.publicHost(r) + a.cfg.Jobi.CallbackPath
}

// authProviders tells the login page which sign-in options exist.
func (a *API) authProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"password": true, "jobi": a.cfg.Jobi.Enabled()})
}

func (a *API) jobiLogin(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.Jobi.Enabled() {
		http.Redirect(w, r, "/login?error=jobi_unavailable", http.StatusSeeOther)
		return
	}
	flow := jobiFlow{State: randomToken(24), Verifier: randomToken(48), Next: safeNext(r.URL.Query().Get("next"))}
	raw, _ := json.Marshal(flow)
	http.SetCookie(w, &http.Cookie{Name: jobiFlowCookie, Value: base64.RawURLEncoding.EncodeToString(raw),
		Path: jobiFlowPath, MaxAge: 600, HttpOnly: true, Secure: a.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	sum := sha256.Sum256([]byte(flow.Verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {a.cfg.Jobi.ClientID}, "redirect_uri": {a.jobiRedirectURI(r)},
		"scope": {a.cfg.Jobi.Scopes}, "state": {flow.State},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	u, err := url.Parse(a.cfg.Jobi.AuthorizeURL)
	if err != nil {
		http.Redirect(w, r, "/login?error=jobi", http.StatusSeeOther)
		return
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (a *API) jobiCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(code string) { http.Redirect(w, r, "/login?error="+code, http.StatusSeeOther) }
	var flow jobiFlow
	c, err := r.Cookie(jobiFlowCookie)
	http.SetCookie(w, &http.Cookie{Name: jobiFlowCookie, Value: "", Path: jobiFlowPath, MaxAge: -1,
		HttpOnly: true, Secure: a.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	if err == nil {
		if raw, derr := base64.RawURLEncoding.DecodeString(c.Value); derr == nil {
			_ = json.Unmarshal(raw, &flow)
		}
	}
	q := r.URL.Query()
	if flow.State == "" || !security.EqualSecret(q.Get("state"), flow.State) {
		fail("jobi_expired")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		fail("jobi_denied")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	profile, err := a.jobiExchange(ctx, r, q.Get("code"), flow.Verifier)
	if err != nil {
		a.log.Warn("jobi sign-in failed", "err", err) // never log codes, tokens or the profile
		fail("jobi")
		return
	}
	u, err := a.jobiUser(r, profile)
	if errors.Is(err, store.ErrDuplicate) {
		fail("jobi_email_taken")
		return
	}
	if err != nil {
		a.log.Error("jobi user", "err", err)
		fail("jobi")
		return
	}
	now := time.Now().UTC()
	_ = a.store.UpdateLoginState(r.Context(), u.ID, 0, nil, &now)
	if old, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), security.HashToken(old.Value)) // rotate the session
	}
	if err := a.startSession(w, r, u); err != nil {
		fail("jobi")
		return
	}
	a.audit(r, "user.login", "user", u.ID, map[string]any{"via": "jobi"})
	next := flow.Next
	if next == "" {
		next = "/dashboard"
		if a.canRecruit(u) {
			next = "/admin"
		}
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// jobiExchange trades the code for a token and returns the employee's profile.
func (a *API) jobiExchange(ctx context.Context, r *http.Request, code, verifier string) (*jobiProfile, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {a.jobiRedirectURI(r)}, "code_verifier": {verifier}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Jobi.TokenURL, strings.NewReader(form.Encode()))
	req.SetBasicAuth(a.cfg.Jobi.ClientID, a.cfg.Jobi.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New("token endpoint unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("token endpoint status " + res.Status)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return nil, errors.New("no access token")
	}
	ireq, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.Jobi.UserinfoURL, nil)
	ireq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	ireq.Header.Set("Accept", "application/json")
	ires, err := http.DefaultClient.Do(ireq)
	if err != nil {
		return nil, errors.New("userinfo unreachable")
	}
	defer ires.Body.Close()
	if ires.StatusCode != http.StatusOK {
		return nil, errors.New("userinfo status " + ires.Status)
	}
	var p jobiProfile
	if err := json.NewDecoder(io.LimitReader(ires.Body, 1<<16)).Decode(&p); err != nil || p.Sub == "" {
		return nil, errors.New("userinfo has no subject")
	}
	return &p, nil
}

// jobiUser finds the account for this JOBI subject, or creates a recruiter account.
func (a *API) jobiUser(r *http.Request, p *jobiProfile) (*domain.User, error) {
	if u, err := a.store.GetUserByJobiSub(r.Context(), p.Sub); err == nil {
		return u, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = "FDC Staff"
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" || !p.EmailVerified {
		email = "jobi-" + p.Sub + "@jobi.invalid" // placeholder; never used to sign in
	}
	u := &domain.User{ID: newID(), Role: domain.RoleRecruiter, FullName: truncateStr(name, 120), Email: email,
		JobiSub: p.Sub, CreatedAt: time.Now().UTC()}
	if err := a.store.CreateUser(r.Context(), u); err != nil {
		return nil, err // ErrDuplicate: this email already has a password account
	}
	a.audit(r, "user.register", "user", u.ID, map[string]any{"via": "jobi"})
	return u, nil
}
