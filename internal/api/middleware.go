package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

const (
	sessionCookie = "ais_session"
	csrfCookie    = "ais_csrf"
	csrfHeader    = "X-CSRF-Token"
)

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
)

func userFrom(r *http.Request) *domain.User {
	u, _ := r.Context().Value(ctxUser).(*domain.User)
	return u
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// recoverer turns panics into a 500 without leaking stack traces to clients.
func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				a.log.Error("panic", "err", fmt.Sprint(v), "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		a.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds(), "ip", a.clientIP(r))
	})
}

// securityHeaders hardens every API response.
func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if a.cfg.CookieSecure {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) rateLimit(rl *security.RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := rl.Allow(a.clientIP(r))
		if !ok {
			w.Header().Set("Retry-After", fmt.Sprint(int(math.Ceil(wait.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Please slow down and try again shortly.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// csrf protects cookie-authenticated, state-changing requests with two checks:
// 1. the Origin (or Referer) must be one of ALLOWED_ORIGINS;
// 2. the X-CSRF-Token header must match the ais_csrf cookie (double-submit).
// SameSite=Lax cookies add a third layer in modern browsers.
func (a *API) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUnsafe(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Host != "" {
				origin = ref.Scheme + "://" + ref.Host
			}
		}
		if origin != "" && !a.originAllowed(r, strings.TrimRight(origin, "/")) {
			writeError(w, http.StatusForbidden, "bad_origin", "Request origin is not allowed.")
			return
		}
		c, err := r.Cookie(csrfCookie)
		if err != nil || !security.EqualSecret(c.Value, r.Header.Get(csrfHeader)) {
			writeError(w, http.StatusForbidden, "csrf", "Security token missing or expired. Please refresh the page and try again.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate loads the user from the session cookie if present. It never rejects;
// use requireUser / requireStaff on routes that need a login.
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || len(c.Value) < 32 || len(c.Value) > 128 {
			next.ServeHTTP(w, r)
			return
		}
		sess, user, err := a.store.GetSessionUser(r.Context(), security.HashToken(c.Value))
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				a.log.Error("session lookup", "err", err)
			}
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, user)
		ctx = context.WithValue(ctx, ctxSession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r) == nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Please log in to continue.")
			return
		}
		next(w, r)
	}
}

func requireStaff(next http.HandlerFunc) http.HandlerFunc {
	return requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r).Role.IsStaff() {
			writeError(w, http.StatusForbidden, "forbidden", "This area is only for recruiters.")
			return
		}
		next(w, r)
	})
}

func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return requireUser(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r).Role != domain.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "Only admins can do this.")
			return
		}
		next(w, r)
	})
}

// requireInternal guards endpoints called by the voice agent with a shared secret.
func (a *API) requireInternal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !security.EqualSecret(token, a.cfg.InternalSecret) {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Invalid internal token.")
			return
		}
		next(w, r)
	}
}

func (a *API) audit(r *http.Request, action, entity, entityID string, detail map[string]any) {
	e := domain.AuditEntry{Action: action, Entity: entity, EntityID: entityID, IP: a.clientIP(r), Detail: detail}
	if u := userFrom(r); u != nil {
		e.UserID = u.ID
	}
	if err := a.store.Audit(context.WithoutCancel(r.Context()), e); err != nil {
		a.log.Warn("audit write failed", "err", err)
	}
}

// originAllowed: the configured origins, plus (single-container deploys) the
// exact origin the request was served on, as reported by our own front server.
func (a *API) originAllowed(r *http.Request, origin string) bool {
	if slices.Contains(a.cfg.AllowedOrigins, origin) {
		return true
	}
	if !a.cfg.SameHostOrigin {
		return false
	}
	host := r.Header.Get("X-Forwarded-Host")
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	proto = strings.TrimSpace(proto)
	if host == "" || (proto != "https" && proto != "http") || (proto == "http" && a.cfg.IsProduction()) {
		return false
	}
	return origin == proto+"://"+host
}
