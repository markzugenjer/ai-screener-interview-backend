// Package api is the HTTP layer: routing, security middleware and handlers.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/config"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/storage"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/worker"
)

type API struct {
	cfg   *config.Config
	store store.Store
	enc   *security.Encryptor
	files storage.Files
	log   *slog.Logger

	// jobs, when set, runs queued jobs inside the request that needs the result
	// (INLINE_JOBS on request-based hosts). Nil means a background worker does it.
	jobs *worker.Worker

	globalLimit    *security.RateLimiter
	authLimit      *security.RateLimiter
	interviewLimit *security.RateLimiter
}

func New(cfg *config.Config, st store.Store, enc *security.Encryptor, files storage.Files, log *slog.Logger) *API {
	return &API{
		cfg: cfg, store: st, enc: enc, files: files, log: log,
		globalLimit: security.NewRateLimiter(300, 120),
		// Room for maxFailedLogins attempts plus the right one before the per-IP limit applies.
		authLimit:      security.NewRateLimiter(20, 12),
		interviewLimit: security.NewRateLimiter(20, 10),
	}
}

// WithInlineJobs makes the API process queued jobs (scoring) itself, inside the
// requests that wait for them, instead of relying on a background worker.
func (a *API) WithInlineJobs(w *worker.Worker) *API {
	a.jobs = w
	return a
}

// runJobs processes queued jobs while the current request stays open. It is
// detached from the client's cancellation so a closed tab cannot abort a
// half-written score, but bounded so the request always ends.
func (a *API) runJobs(r *http.Request) {
	if a.jobs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 4*time.Minute+30*time.Second)
	defer cancel()
	a.jobs.Drain(ctx, 2)
}

// Handler builds the route table (Go 1.22+ method + path patterns) and wraps it in middleware.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.Handler { return a.rateLimit(a.authLimit, h) }
	iv := func(h http.HandlerFunc) http.Handler { return a.rateLimit(a.interviewLimit, h) }

	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.ready)

	// Auth
	mux.HandleFunc("GET /api/v1/auth/csrf", a.csrfToken)
	mux.Handle("POST /api/v1/auth/register", auth(a.register))
	mux.Handle("POST /api/v1/auth/login", auth(a.login))
	mux.HandleFunc("POST /api/v1/auth/logout", a.logout)
	mux.HandleFunc("GET /api/v1/auth/me", requireUser(a.me))

	// Jobs (public)
	mux.HandleFunc("GET /api/v1/jobs", a.listOpenJobs)
	mux.HandleFunc("GET /api/v1/jobs/{slug}", a.getJob)

	// Candidate interviews
	mux.HandleFunc("GET /api/v1/me/interviews", requireUser(a.myInterviews))
	mux.Handle("POST /api/v1/jobs/{slug}/interviews", iv(requireUser(a.startInterview)))
	mux.HandleFunc("GET /api/v1/interviews/{id}", requireUser(a.getInterview))
	mux.Handle("POST /api/v1/interviews/{id}/token", iv(requireUser(a.interviewToken)))
	mux.Handle("POST /api/v1/interviews/{id}/reset", iv(requireUser(a.resetInterview)))
	mux.Handle("POST /api/v1/interviews/{id}/finish", iv(requireUser(a.finishInterview)))
	mux.HandleFunc("POST /api/v1/interviews/{id}/events", requireUser(a.interviewEvent))
	mux.HandleFunc("GET /api/v1/interviews/{id}/feedback", requireUser(a.feedback))
	mux.Handle("POST /api/v1/interviews/{id}/score", iv(requireUser(a.scoreNow)))

	// Recruiter / admin
	mux.HandleFunc("GET /api/v1/admin/stats", requireStaff(a.adminStats))
	mux.HandleFunc("GET /api/v1/admin/jobs", requireStaff(a.adminListJobs))
	mux.HandleFunc("POST /api/v1/admin/jobs", requireStaff(a.adminCreateJob))
	mux.HandleFunc("GET /api/v1/admin/jobs/{id}", requireStaff(a.adminGetJob))
	mux.HandleFunc("PUT /api/v1/admin/jobs/{id}", requireStaff(a.adminUpdateJob))
	mux.HandleFunc("DELETE /api/v1/admin/jobs/{id}", requireAdmin(a.adminDeleteJob))
	mux.HandleFunc("GET /api/v1/admin/results", requireStaff(a.adminResults))
	mux.HandleFunc("GET /api/v1/admin/results/{id}", requireStaff(a.adminResult))
	mux.HandleFunc("POST /api/v1/admin/results/{id}/review", requireStaff(a.adminReview))
	mux.HandleFunc("POST /api/v1/admin/results/{id}/rescore", requireStaff(a.adminRescore))
	mux.HandleFunc("GET /api/v1/admin/candidates/{id}/resume", requireStaff(a.adminResume))

	// Internal endpoints for the voice agent (shared secret, no cookies, no CSRF).
	internal := http.NewServeMux()
	internal.HandleFunc("GET /internal/interviews/{id}/context", a.requireInternal(a.internalContext))
	internal.HandleFunc("POST /internal/interviews/{id}/turns", a.requireInternal(a.internalTurns))
	internal.HandleFunc("POST /internal/interviews/{id}/complete", a.requireInternal(a.internalComplete))

	public := a.authenticate(a.csrf(mux))

	root := http.NewServeMux()
	root.Handle("/internal/", internal)
	root.Handle("/", public)

	return a.recoverer(a.logRequests(a.securityHeaders(a.rateLimit(a.globalLimit, root))))
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "Database is not reachable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
