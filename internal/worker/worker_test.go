package worker

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store/memory"
)

type countingLLM struct{ calls atomic.Int32 }

func (c *countingLLM) Complete(context.Context, string, string, int) (string, error) {
	c.calls.Add(1)
	return `{"questions":[],"ratings":{"technical":4,"problemSolving":4,"communication":4,"motivation":4},"notes":{},
	"recruiterSummary":"ok","candidate":{"en":{"summary":"Nice work."},"id":{"summary":"Kerja bagus."}}}`, nil
}
func (c *countingLLM) Model() string { return "fake" }

// Drain scores a completed interview inside the caller (request-scoped hosts),
// and a second Drain finds nothing left, so the model is called exactly once.
func TestDrainScoresInline(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	job := &domain.Job{ID: "11111111-1111-4111-8111-111111111111", Title: "FE", Slug: "fe", Level: "junior", Weights: domain.DefaultWeights(), Status: domain.JobOpen}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	iv := &domain.Interview{ID: "22222222-2222-4222-8222-222222222222", JobID: job.ID, UserID: "u1", Status: domain.InterviewCompleted, RoomName: "r1"}
	if err := st.CreateInterview(ctx, iv); err != nil {
		t.Fatal(err)
	}
	answer := strings.Repeat("saya biasanya bekerja dalam tim dan suka belajar ", 6)
	if err := st.AppendTurns(ctx, iv.ID, []domain.Turn{{Speaker: "ai", Text: "Ceritakan dirimu"}, {Speaker: "candidate", Text: answer, StartMs: 1000, EndMs: 30000}}); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueScoring(ctx, st, iv.ID); err != nil {
		t.Fatal(err)
	}
	llm := &countingLLM{}
	w := &Worker{Store: st, LLM: llm, Language: "English", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n := w.Drain(ctx, 2); n != 1 {
		t.Fatalf("drained %d jobs, want 1", n)
	}
	got, _ := st.GetInterview(ctx, iv.ID)
	if got.Status != domain.InterviewScored {
		t.Fatalf("status %s", got.Status)
	}
	if n := w.Drain(ctx, 2); n != 0 || llm.calls.Load() != 1 {
		t.Fatalf("second drain handled %d jobs, model calls %d", n, llm.calls.Load())
	}
}
