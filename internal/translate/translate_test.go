package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
)

type fake struct {
	reply  string
	system string
}

func (f *fake) Complete(_ context.Context, system, _ string, _ int) (string, error) {
	f.system = system
	return f.reply, nil
}
func (f *fake) Model() string { return "fake" }

func TestJobTranslation(t *testing.T) {
	f := &fake{reply: "```json\n{\"title\":\" Clinic Staff \",\"division\":\"Operations\",\"category\":\"Non-Technical\",\"location\":\"Jakarta\",\"employmentType\":\"Full-time\",\"description\":\"FDC Dental Clinic is looking for...\",\"requirements\":[\"High school diploma\",\"  \"]}\n```"}
	in := domain.JobText{Title: "Staff Klinik", Division: "Operasional", Requirements: []string{"Minimal SMA"}}
	out, err := Job(context.Background(), f, "id", "en", in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Clinic Staff" || out.Division != "Operations" || len(out.Requirements) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(f.system, "from Bahasa Indonesia to English") {
		t.Fatalf("prompt: %s", f.system)
	}
	if _, err := Job(context.Background(), f, "id", "id", in); err == nil {
		t.Fatal("same-language translation must be refused")
	}
	f.reply = `{"title":"","requirements":[]}`
	if _, err := Job(context.Background(), f, "id", "en", in); err == nil {
		t.Fatal("an empty translation must be rejected")
	}
}
