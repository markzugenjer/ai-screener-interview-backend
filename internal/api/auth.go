package api

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

const (
	maxFailedLogins = 10 // wrong passwords in a row before the account locks
	lockoutDuration = 15 * time.Minute
)

// csrfToken issues the double-submit token. The frontend calls this once and
// then sends the value in the X-CSRF-Token header on every POST/PUT/DELETE.
func (a *API) csrfToken(w http.ResponseWriter, r *http.Request) {
	token := ""
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 && len(c.Value) <= 64 {
		token = c.Value
	} else {
		var err error
		if token, err = security.RandomToken(32); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "Could not create a security token.")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/", MaxAge: int((24 * time.Hour).Seconds()),
		Secure: a.cfg.CookieSecure, HttpOnly: false, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (a *API) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		MaxAge: int(time.Until(expires).Seconds()), Secure: a.cfg.CookieSecure,
		HttpOnly: true, // JavaScript can never read the session token
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: a.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	token, err := security.RandomToken(32)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	s := &domain.Session{ID: newID(), UserID: u.ID, TokenHash: security.HashToken(token),
		UserAgent: truncateStr(r.UserAgent(), 300), IP: a.clientIP(r), ExpiresAt: now.Add(a.cfg.SessionTTL), CreatedAt: now}
	if err := a.store.CreateSession(r.Context(), s); err != nil {
		return err
	}
	a.setSessionCookie(w, token, s.ExpiresAt)
	return nil
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxResumeBytes+(1<<20))
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeFieldErrors(w, security.FieldErrors{"resume": fmt.Sprintf("Resume must be %d MB or smaller.", a.cfg.MaxResumeBytes>>20)})
			return
		}
		writeError(w, http.StatusBadRequest, "bad_form", "Send the form as multipart/form-data.")
		return
	}
	defer r.MultipartForm.RemoveAll()

	fullName := strings.Join(strings.Fields(r.FormValue("fullName")), " ")
	email := security.NormalizeEmail(r.FormValue("email"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirmPassword")
	phoneRaw := r.FormValue("whatsapp")

	fe := security.FieldErrors{}
	if !security.ValidFullName(fullName) {
		fe["fullName"] = "Enter your full name (letters only, 2–100 characters)."
	}
	if !security.ValidEmail(email) {
		fe["email"] = "Enter a valid email address."
	}
	if p := security.PasswordProblem(password, email); p != "" {
		fe["password"] = p
	}
	if confirm != password {
		fe["confirmPassword"] = "Passwords do not match."
	}
	phone, err := security.NormalizeWhatsApp(phoneRaw)
	if err != nil {
		fe["whatsapp"] = "Enter a valid WhatsApp number, e.g. 0812 3456 7890."
	}

	var resume []byte
	var resumeMime, resumeExt, resumeName string
	file, hdr, err := r.FormFile("resume")
	if err != nil {
		fe["resume"] = "Upload your resume (PDF or DOCX)."
	} else {
		defer file.Close()
		resume, err = io.ReadAll(io.LimitReader(file, a.cfg.MaxResumeBytes+1))
		switch {
		case err != nil:
			fe["resume"] = "Could not read the uploaded file."
		case int64(len(resume)) > a.cfg.MaxResumeBytes:
			fe["resume"] = fmt.Sprintf("Resume must be %d MB or smaller.", a.cfg.MaxResumeBytes>>20)
		case len(resume) == 0:
			fe["resume"] = "The uploaded file is empty."
		default:
			resumeMime, resumeExt, err = security.SniffResume(resume)
			if err != nil {
				fe["resume"] = "Resume must be a PDF or DOCX file."
			}
			resumeName = security.SafeFilename(hdr.Filename)
		}
	}
	if fe.Any() {
		writeFieldErrors(w, fe)
		return
	}

	if _, err := a.store.GetUserByEmail(r.Context(), email); err == nil {
		writeFieldErrors(w, security.FieldErrors{"email": "An account with this email already exists. Try logging in."})
		return
	}

	hash, err := security.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not create your account.")
		return
	}
	u := &domain.User{ID: newID(), Role: domain.RoleCandidate, FullName: fullName, Email: email,
		PasswordHash: hash, ResumeName: resumeName, ResumeMime: resumeMime, CreatedAt: time.Now().UTC()}
	if u.PhoneEnc, err = a.enc.EncryptString(phone, "phone:"+u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not create your account.")
		return
	}
	if u.ResumeKey, err = a.files.Put(r.Context(), resume, resumeExt, u.ID); err != nil {
		a.log.Error("store resume", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not save your resume. Please try again.")
		return
	}
	if err := a.store.CreateUser(r.Context(), u); err != nil {
		_ = a.files.Delete(r.Context(), u.ResumeKey)
		if errors.Is(err, store.ErrDuplicate) {
			writeFieldErrors(w, security.FieldErrors{"email": "An account with this email already exists. Try logging in."})
			return
		}
		a.log.Error("create user", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not create your account.")
		return
	}
	if err := a.startSession(w, r, u); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Account created, but logging in failed. Please log in.")
		return
	}
	a.audit(r, "user.register", "user", u.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"user": a.toUserDTO(u)})
}

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if !readJSON(w, r, &in) {
		return
	}
	email := security.NormalizeEmail(in.Email)
	if email == "" || in.Password == "" || len(in.Password) > 256 {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	u, err := a.store.GetUserByEmail(r.Context(), email)
	if err != nil {
		security.DummyVerify(in.Password) // same timing whether or not the email exists
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	now := time.Now().UTC()
	if u.LockedUntil != nil && u.LockedUntil.After(now) {
		mins := int(math.Ceil(time.Until(*u.LockedUntil).Minutes()))
		writeError(w, http.StatusTooManyRequests, "locked",
			fmt.Sprintf("Too many failed attempts. Try again in %d minute(s).", mins))
		return
	}
	if !security.VerifyPassword(in.Password, u.PasswordHash) {
		failed := u.FailedLogins + 1
		var lock *time.Time
		if failed >= maxFailedLogins {
			t := now.Add(lockoutDuration)
			lock, failed = &t, 0
			a.audit(r, "user.locked", "user", u.ID, nil)
		}
		_ = a.store.UpdateLoginState(r.Context(), u.ID, failed, lock, nil)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	_ = a.store.UpdateLoginState(r.Context(), u.ID, 0, nil, &now)
	// A new session token on every login prevents session fixation.
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), security.HashToken(c.Value))
	}
	if err := a.startSession(w, r, u); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not log you in.")
		return
	}
	a.audit(r, "user.login", "user", u.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": a.toUserDTO(u)})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), security.HashToken(c.Value))
	}
	a.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": a.toUserDTO(userFrom(r))})
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
