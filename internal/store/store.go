// Package store defines the persistence interface. Two implementations exist:
// postgres (production) and memory (tests and quick local runs).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("already exists")
)

type JobFilter struct {
	Status   string // empty = any
	Division string
	Query    string
}

type Store interface {
	// Users and sessions
	CreateUser(ctx context.Context, u *domain.User) error
	GetUserByEmail(ctx context.Context, email string) (*domain.User, error)
	GetUserByID(ctx context.Context, id string) (*domain.User, error)
	UpdateLoginState(ctx context.Context, userID string, failed int, lockedUntil, lastLogin *time.Time) error
	SetUserRole(ctx context.Context, userID string, role domain.Role) error

	CreateSession(ctx context.Context, s *domain.Session) error
	GetSessionUser(ctx context.Context, tokenHash string) (*domain.Session, *domain.User, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteExpiredSessions(ctx context.Context) (int64, error)

	// Jobs and questions
	ListJobs(ctx context.Context, f JobFilter) ([]domain.Job, error)
	GetJob(ctx context.Context, id string) (*domain.Job, error)
	GetJobBySlug(ctx context.Context, slug string) (*domain.Job, error)
	CreateJob(ctx context.Context, j *domain.Job) error
	UpdateJob(ctx context.Context, j *domain.Job) error
	DeleteJob(ctx context.Context, id string) error
	ListQuestions(ctx context.Context, jobID string) ([]domain.Question, error)
	ReplaceQuestions(ctx context.Context, jobID string, qs []domain.Question) error

	// Interviews
	CreateInterview(ctx context.Context, iv *domain.Interview) error
	GetInterview(ctx context.Context, id string) (*domain.Interview, error)
	UpdateInterview(ctx context.Context, iv *domain.Interview) error
	CountCompletedAttempts(ctx context.Context, userID, jobID string) (int, error)
	CountAttempts(ctx context.Context, userID, jobID string) (int, error)
	ListUserInterviews(ctx context.Context, userID string) ([]domain.InterviewSummary, error)
	AppendTurns(ctx context.Context, interviewID string, turns []domain.Turn) error
	ListTurns(ctx context.Context, interviewID string) ([]domain.Turn, error)
	ClearTurns(ctx context.Context, interviewID string) error

	// Scores, reviews and results
	SaveScore(ctx context.Context, s *domain.Score) error
	GetScore(ctx context.Context, interviewID string) (*domain.Score, error)
	AddReview(ctx context.Context, r *domain.Review) error
	ListReviews(ctx context.Context, interviewID string) ([]domain.Review, error)
	ListResults(ctx context.Context, f domain.ResultFilter) ([]domain.InterviewSummary, int, error)
	Stats(ctx context.Context) (*domain.Stats, error)

	// Background job queue
	Enqueue(ctx context.Context, kind, payload string, runAfter time.Time) error
	ClaimJob(ctx context.Context) (*domain.QueueJob, error) // returns ErrNotFound when the queue is empty
	CompleteJob(ctx context.Context, id string) error
	FailJob(ctx context.Context, id string, errMsg string, retryAt *time.Time) error

	Audit(ctx context.Context, e domain.AuditEntry) error
	Ping(ctx context.Context) error
	Close()
}
