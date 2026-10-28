// Package worker processes background jobs from the Postgres-backed queue.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/llm"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/scoring"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

const (
	KindScoreInterview = "score_interview"
	maxAttempts        = 3
)

type ScorePayload struct {
	InterviewID string `json:"interviewId"`
}

type Worker struct {
	Store    store.Store
	LLM      llm.Completer
	Language string
	Log      *slog.Logger
	Poll     time.Duration
}

func EnqueueScoring(ctx context.Context, s store.Store, interviewID string) error {
	b, _ := json.Marshal(ScorePayload{InterviewID: interviewID})
	return s.Enqueue(ctx, KindScoreInterview, string(b), time.Now())
}

// Run blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	if w.Poll == 0 {
		w.Poll = 2 * time.Second
	}
	w.Log.Info("worker started")
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			w.Log.Info("worker stopped")
			return
		case <-cleanup.C:
			if n, err := w.Store.DeleteExpiredSessions(ctx); err == nil && n > 0 {
				w.Log.Info("deleted expired sessions", "count", n)
			}
		default:
		}

		job, err := w.Store.ClaimJob(ctx)
		if errors.Is(err, store.ErrNotFound) {
			select {
			case <-ctx.Done():
			case <-time.After(w.Poll):
			}
			continue
		}
		if err != nil {
			w.Log.Error("claim job", "err", err)
			time.Sleep(w.Poll)
			continue
		}
		w.handle(ctx, job)
	}
}

func (w *Worker) handle(ctx context.Context, job *domain.QueueJob) {
	jctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var err error
	switch job.Kind {
	case KindScoreInterview:
		err = w.scoreInterview(jctx, job)
	default:
		err = fmt.Errorf("unknown job kind %q", job.Kind)
	}
	if err == nil {
		_ = w.Store.CompleteJob(ctx, job.ID)
		return
	}
	w.Log.Error("job failed", "kind", job.Kind, "id", job.ID, "attempt", job.Attempts, "err", err)
	if job.Attempts < maxAttempts && !errors.Is(err, llm.ErrNotConfigured) {
		retry := time.Now().Add(time.Duration(job.Attempts) * time.Minute)
		_ = w.Store.FailJob(ctx, job.ID, err.Error(), &retry)
		return
	}
	_ = w.Store.FailJob(ctx, job.ID, err.Error(), nil)
	if job.Kind == KindScoreInterview {
		w.markFailed(ctx, job, err)
	}
}

func (w *Worker) markFailed(ctx context.Context, job *domain.QueueJob, cause error) {
	var p ScorePayload
	if json.Unmarshal([]byte(job.Payload), &p) != nil {
		return
	}
	iv, err := w.Store.GetInterview(ctx, p.InterviewID)
	if err != nil {
		return
	}
	iv.Status = domain.InterviewFailed
	iv.LastError = cause.Error()
	_ = w.Store.UpdateInterview(ctx, iv)
}

func (w *Worker) scoreInterview(ctx context.Context, job *domain.QueueJob) error {
	var p ScorePayload
	if err := json.Unmarshal([]byte(job.Payload), &p); err != nil {
		return err
	}
	iv, err := w.Store.GetInterview(ctx, p.InterviewID)
	if err != nil {
		return err
	}
	if iv.Status == domain.InterviewScored {
		return nil // already done (duplicate job)
	}
	j, err := w.Store.GetJob(ctx, iv.JobID)
	if err != nil {
		return err
	}
	qs, err := w.Store.ListQuestions(ctx, iv.JobID)
	if err != nil {
		return err
	}
	turns, err := w.Store.ListTurns(ctx, iv.ID)
	if err != nil {
		return err
	}
	sc, err := scoring.Score(ctx, w.LLM, iv.ID, scoring.Input{Job: *j, Questions: qs, Turns: turns, Language: w.Language})
	if err != nil {
		return err
	}
	if err := w.Store.SaveScore(ctx, sc); err != nil {
		return err
	}
	iv.Status = domain.InterviewScored
	iv.LastError = ""
	if err := w.Store.UpdateInterview(ctx, iv); err != nil {
		return err
	}
	w.Log.Info("interview scored", "interview", iv.ID, "overall", sc.Overall)
	return nil
}
