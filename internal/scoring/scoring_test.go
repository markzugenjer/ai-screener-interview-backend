package scoring

import (
	"context"
	"strings"
	"testing"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
)

type fakeLLM struct {
	reply  string
	called bool
}

func (f *fakeLLM) Complete(context.Context, string, string, int) (string, error) {
	f.called = true
	return f.reply, nil
}
func (f *fakeLLM) Model() string { return "fake" }

func turns() []domain.Turn {
	answer := strings.Repeat("saya biasanya memakai let dan const karena block scoped eee ", 5)
	return []domain.Turn{
		{Speaker: "ai", Text: "Apa perbedaan let, const, dan var?", StartMs: 0, EndMs: 4000},
		{Speaker: "candidate", Text: answer, StartMs: 5000, EndMs: 25000},
		{Speaker: "ai", Text: "Terima kasih. Pertanyaan berikutnya.", StartMs: 26000, EndMs: 29000},
		{Speaker: "candidate", Text: answer, StartMs: 37000, EndMs: 57000},
	}
}

func TestSpeechMetrics(t *testing.T) {
	m := SpeechMetricsFor(turns())
	if m.CandidateWords != 100 {
		t.Fatalf("words = %d", m.CandidateWords)
	}
	if m.FillerWords != 10 {
		t.Fatalf("fillers = %d", m.FillerWords)
	}
	if m.LongPauses != 1 { // 37000 - 29000 = 8s
		t.Fatalf("long pauses = %d", m.LongPauses)
	}
	if m.WordsPerMinute != 150 {
		t.Fatalf("wpm = %v", m.WordsPerMinute)
	}
	if m.Rating != 3.5 { // pace 5, fillers 10% -> -1.5
		t.Fatalf("rating = %v", m.Rating)
	}
}

func TestScoreCombinesWeights(t *testing.T) {
	f := &fakeLLM{reply: "Here you go:\n```json\n" + `{"questions":[{"questionId":"q1","score":4,"coveredPoints":["block scope"],"missedPoints":[],"evidence":"let dan const block scoped","reasoning":"ok"},{"questionId":"invented","score":5}],
	"ratings":{"technical":5,"problemSolving":3,"communication":4,"motivation":1},
	"notes":{"technical":["Explained scope well."],"problemSolving":[],"communication":[],"motivation":[]},
	"recruiterSummary":"Solid basics.",
	"candidate":{"en":{"summary":"You explained scope clearly.","strengths":["Clear"],"improvements":["Give examples"],"tips":["Practice async"],"notes":{"technical":["You explained scope well."]}},
	"id":{"summary":"Kamu menjelaskan scope dengan jelas.","strengths":["Jelas"],"improvements":["Beri contoh"],"tips":["Latihan async"],"notes":{"technical":["Penjelasan scope kamu bagus."]}}}}` + "\n```"}
	job := domain.Job{Title: "FE", Level: "junior", Weights: domain.DefaultWeights()}
	qs := []domain.Question{{ID: "q1", Skill: "JavaScript", Text: "let vs var"}}
	sc, err := Score(context.Background(), f, "iv1", Input{Job: job, Questions: qs, Turns: turns(), Language: "English"})
	if err != nil {
		t.Fatal(err)
	}
	// technical 40*1 + problemSolving 20*.5 + communication 20*.75 + speech 10*.625 + motivation 0 = 71.25
	if sc.Overall != 71 {
		t.Fatalf("overall = %d", sc.Overall)
	}
	if sc.Recommendation != "yes" {
		t.Fatalf("recommendation = %s", sc.Recommendation)
	}
	if len(sc.QuestionScores) != 1 {
		t.Fatalf("invented questions must be dropped, got %d", len(sc.QuestionScores))
	}
	if sc.CandidateSummary != "You explained scope clearly." || sc.Tips[0] != "Practice async" {
		t.Fatalf("legacy fields should mirror English: %q %v", sc.CandidateSummary, sc.Tips)
	}
	id := sc.CandidateFor("id")
	if id.Summary != "Kamu menjelaskan scope dengan jelas." || id.Growth[0] != "Beri contoh" {
		t.Fatalf("indonesian feedback = %+v", id)
	}
	if id.Notes["technical"][0] != "Penjelasan scope kamu bagus." || !strings.HasPrefix(id.Notes["speech"][0], "Berbicara sekitar 150 kata") {
		t.Fatalf("indonesian notes = %v", id.Notes)
	}
	if !strings.HasPrefix(sc.CandidateFor("en").Notes["speech"][0], "Spoke at about 150") {
		t.Fatal("english speech note missing")
	}
	sum := 0
	for _, d := range sc.Dimensions {
		sum += d.Max
	}
	if sum != 100 {
		t.Fatalf("dimension max sum = %d", sum)
	}
}

func TestInsufficientAnswersSkipsLLM(t *testing.T) {
	f := &fakeLLM{}
	short := []domain.Turn{{Speaker: "ai", Text: "Halo"}, {Speaker: "candidate", Text: "halo juga"}}
	sc, err := Score(context.Background(), f, "iv", Input{Job: domain.Job{Weights: domain.DefaultWeights()}, Turns: short})
	if err != nil {
		t.Fatal(err)
	}
	if f.called || sc.Overall != 0 || len(sc.Tips) == 0 {
		t.Fatal("expected insufficient result without calling the model")
	}
	if !strings.HasPrefix(sc.CandidateFor("id").Summary, "Sesi kamu") || len(sc.CandidateFor("id").Notes["speech"]) != 1 {
		t.Fatal("expected Indonesian insufficient text")
	}
}

func TestCandidateForFallsBackToLegacyFields(t *testing.T) {
	old := &domain.Score{CandidateSummary: "Legacy", Tips: []string{"t"},
		Dimensions: []domain.Dimension{{Key: "technical", Notes: []string{"n"}}}}
	if got := old.CandidateFor("id"); got.Summary != "Legacy" || got.Notes["technical"][0] != "n" {
		t.Fatalf("fallback = %+v", got)
	}
}

func TestRecommendationThresholds(t *testing.T) {
	for overall, want := range map[int]string{95: "strong_yes", 80: "strong_yes", 79: "yes", 65: "yes", 64: "maybe", 50: "maybe", 49: "no", 0: "no"} {
		if got := Recommendation(overall); got != want {
			t.Errorf("%d: %s want %s", overall, got, want)
		}
	}
}
