// Package scoring turns an interview transcript into scores, evidence and feedback.
//
// Pipeline: speech metrics are computed in code from turn timestamps; Claude grades
// every answer against the question's expected points and rates the holistic
// dimensions; then the weighted 0–100 score and recommendation are computed here,
// so the maths is deterministic and auditable rather than left to the model.
package scoring

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/llm"
)

const minCandidateWords = 25

type Input struct {
	Job       domain.Job
	Questions []domain.Question
	Turns     []domain.Turn
	Language  string
}

type llmResult struct {
	Questions []struct {
		QuestionID    string   `json:"questionId"`
		Score         float64  `json:"score"`
		CoveredPoints []string `json:"coveredPoints"`
		MissedPoints  []string `json:"missedPoints"`
		Evidence      string   `json:"evidence"`
		Reasoning     string   `json:"reasoning"`
	} `json:"questions"`
	Ratings struct {
		Technical      float64 `json:"technical"`
		ProblemSolving float64 `json:"problemSolving"`
		Communication  float64 `json:"communication"`
		Motivation     float64 `json:"motivation"`
	} `json:"ratings"`
	Notes struct {
		Technical      []string `json:"technical"`
		ProblemSolving []string `json:"problemSolving"`
		Communication  []string `json:"communication"`
		Motivation     []string `json:"motivation"`
	} `json:"notes"`
	RecruiterSummary string                   `json:"recruiterSummary"`
	Candidate        map[string]candidateText `json:"candidate"` // by language: "en", "id"
}

// candidateText is what the model writes for the candidate, in one language.
type candidateText struct {
	Summary      string              `json:"summary"`
	Strengths    []string            `json:"strengths"`
	Improvements []string            `json:"improvements"`
	Tips         []string            `json:"tips"`
	Notes        map[string][]string `json:"notes"`
}

// Score runs the full pipeline. The interviewID is copied into the result.
func Score(ctx context.Context, c llm.Completer, interviewID string, in Input) (*domain.Score, error) {
	speech := SpeechMetricsFor(in.Turns)
	w := in.Job.Weights
	if w.Sum() != 100 {
		w = domain.DefaultWeights()
	}

	if speech.CandidateWords < minCandidateWords {
		return insufficient(interviewID, w, speech, c.Model()), nil
	}

	reply, err := c.Complete(ctx, systemPrompt(in.Language), userPrompt(in), 8192)
	if err != nil {
		return nil, err
	}
	raw, err := llm.ExtractJSON(reply)
	if err != nil {
		return nil, err
	}
	var r llmResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("parse model JSON: %w", err)
	}

	byID := map[string]domain.Question{}
	for _, q := range in.Questions {
		byID[q.ID] = q
	}
	var qs []domain.QuestionScore
	for _, q := range r.Questions {
		src, ok := byID[q.QuestionID]
		if !ok {
			continue // ignore anything the model invented
		}
		qs = append(qs, domain.QuestionScore{
			QuestionID: q.QuestionID, Question: src.Text, Skill: src.Skill,
			Score: int(math.Round(clamp(q.Score, 1, 5))), CoveredPoints: trimList(q.CoveredPoints, 6),
			MissedPoints: trimList(q.MissedPoints, 6), Evidence: truncate(q.Evidence, 300),
			Reasoning: truncate(q.Reasoning, 500),
		})
	}

	dims := []domain.Dimension{
		dim("technical", "Technical Knowledge", w.Technical, r.Ratings.Technical, r.Notes.Technical),
		dim("problemSolving", "Problem Solving", w.ProblemSolving, r.Ratings.ProblemSolving, r.Notes.ProblemSolving),
		dim("communication", "Communication", w.Communication, r.Ratings.Communication, r.Notes.Communication),
		dim("speech", "Speech Delivery", w.Speech, speech.Rating, speechNotes(speech, "en")),
		dim("motivation", "Motivation & Attitude", w.Motivation, r.Ratings.Motivation, r.Notes.Motivation),
	}

	// Candidate feedback per language. Speech notes come from code, not the model.
	loc := map[string]domain.CandidateText{}
	for _, lang := range domain.FeedbackLanguages {
		ct, ok := r.Candidate[lang]
		if !ok || strings.TrimSpace(ct.Summary) == "" {
			continue
		}
		notes := map[string][]string{}
		for _, d := range dims {
			if d.Key == "speech" {
				notes[d.Key] = speechNotes(speech, lang)
			} else {
				notes[d.Key] = trimList(ct.Notes[d.Key], 4)
			}
		}
		loc[lang] = domain.CandidateText{
			Summary: truncate(ct.Summary, 1200), Strengths: trimList(ct.Strengths, 5),
			Growth: trimList(ct.Improvements, 5), Tips: trimList(ct.Tips, 5), Notes: notes,
		}
	}
	// The single-language fields mirror English (or the first language present).
	var base domain.CandidateText
	for _, lang := range domain.FeedbackLanguages {
		if ct, ok := loc[lang]; ok {
			base = ct
			break
		}
	}
	overall := 0.0
	for _, d := range dims {
		overall += d.Points
	}
	o := int(math.Round(overall))

	return &domain.Score{
		InterviewID: interviewID, Overall: o, Dimensions: dims, QuestionScores: qs, Speech: speech,
		RecruiterSummary: truncate(r.RecruiterSummary, 1200), CandidateSummary: base.Summary,
		Strengths: orEmpty(base.Strengths), Improvements: orEmpty(base.Growth), Tips: orEmpty(base.Tips),
		Localized: loc, Recommendation: Recommendation(o), Model: c.Model(), CreatedAt: time.Now().UTC(),
	}, nil
}

// Recommendation maps the overall score to a label (thresholds from the blueprint).
func Recommendation(overall int) string {
	switch {
	case overall >= 80:
		return "strong_yes"
	case overall >= 65:
		return "yes"
	case overall >= 50:
		return "maybe"
	}
	return "no"
}

func dim(key, label string, weight int, rating float64, notes []string) domain.Dimension {
	rating = clamp(rating, 1, 5)
	points := float64(weight) * (rating - 1) / 4
	return domain.Dimension{Key: key, Label: label, Max: weight, Rating: round1(rating),
		Points: round1(points), Notes: trimList(notes, 4)}
}

var insufficientText = map[string]domain.CandidateText{
	"en": {
		Summary: "Your session ended before we could hear enough of your answers to give a meaningful evaluation. That's okay — you can try again whenever you're ready.",
		Tips: []string{
			"Find a quiet place and check your microphone before starting.",
			"Take a moment to think, then answer in full sentences — there's no rush.",
			"If a question is unclear, ask the interviewer to repeat or rephrase it.",
		},
	},
	"id": {
		Summary: "Sesi kamu berakhir sebelum kami mendengar cukup banyak jawaban untuk memberi penilaian yang berarti. Tidak apa-apa — kamu bisa mencoba lagi kapan pun kamu siap.",
		Tips: []string{
			"Cari tempat yang tenang dan cek mikrofon sebelum mulai.",
			"Luangkan waktu sejenak untuk berpikir, lalu jawab dengan kalimat lengkap — tidak perlu terburu-buru.",
			"Kalau pertanyaannya kurang jelas, minta pewawancara mengulang atau menjelaskannya dengan cara lain.",
		},
	},
}

var insufficientNote = map[string]string{
	"en": "Not enough spoken answers were recorded to evaluate this area.",
	"id": "Belum cukup jawaban lisan yang terekam untuk menilai aspek ini.",
}

func insufficient(id string, w domain.Weights, sp domain.SpeechMetrics, model string) *domain.Score {
	dimKeys := []string{"technical", "problemSolving", "communication", "speech", "motivation"}
	loc := map[string]domain.CandidateText{}
	for _, lang := range domain.FeedbackLanguages {
		ct := insufficientText[lang]
		ct.Strengths, ct.Growth = []string{}, []string{}
		ct.Notes = map[string][]string{}
		for _, k := range dimKeys {
			ct.Notes[k] = []string{insufficientNote[lang]}
		}
		loc[lang] = ct
	}
	note := []string{insufficientNote["en"]}
	return &domain.Score{
		InterviewID: id, Overall: 0, Speech: sp, Model: model, Recommendation: "no", CreatedAt: time.Now().UTC(),
		Dimensions: []domain.Dimension{
			{Key: "technical", Label: "Technical Knowledge", Max: w.Technical, Rating: 1, Notes: note},
			{Key: "problemSolving", Label: "Problem Solving", Max: w.ProblemSolving, Rating: 1, Notes: note},
			{Key: "communication", Label: "Communication", Max: w.Communication, Rating: 1, Notes: note},
			{Key: "speech", Label: "Speech Delivery", Max: w.Speech, Rating: 1, Notes: note},
			{Key: "motivation", Label: "Motivation & Attitude", Max: w.Motivation, Rating: 1, Notes: note},
		},
		RecruiterSummary: "The interview ended before the candidate gave enough answers to evaluate.",
		CandidateSummary: loc["en"].Summary, Strengths: []string{}, Improvements: []string{}, Tips: loc["en"].Tips,
		Localized: loc,
	}
}

// ---------- Speech metrics (no LLM) ----------

var wordRe = regexp.MustCompile(`[\p{L}\p{N}']+`)

var fillers = map[string]bool{
	"eee": true, "ee": true, "eh": true, "ehm": true, "emm": true, "em": true, "hmm": true,
	"hm": true, "umm": true, "um": true, "uh": true, "anu": true, "mmm": true,
}

func SpeechMetricsFor(turns []domain.Turn) domain.SpeechMetrics {
	var m domain.SpeechMetrics
	var prevAIEnd int
	for _, t := range turns {
		if t.Speaker == "ai" {
			prevAIEnd = t.EndMs
			continue
		}
		words := wordRe.FindAllString(strings.ToLower(t.Text), -1)
		m.CandidateWords += len(words)
		for _, w := range words {
			if fillers[w] {
				m.FillerWords++
			}
		}
		m.FillerWords += strings.Count(strings.ToLower(t.Text), "apa ya")
		if t.EndMs > t.StartMs {
			m.SpeakingSeconds += float64(t.EndMs-t.StartMs) / 1000
		}
		if prevAIEnd > 0 && t.StartMs-prevAIEnd > 6000 {
			m.LongPauses++
		}
		prevAIEnd = 0
	}
	if m.SpeakingSeconds >= 20 {
		m.WordsPerMinute = round1(float64(m.CandidateWords) / (m.SpeakingSeconds / 60))
	}
	m.SpeakingSeconds = round1(m.SpeakingSeconds)
	m.Rating = speechRating(m)
	return m
}

func speechRating(m domain.SpeechMetrics) float64 {
	if m.CandidateWords < minCandidateWords {
		return 1
	}
	r := 3.0 // unknown pace stays neutral
	if wpm := m.WordsPerMinute; wpm > 0 {
		switch {
		case wpm >= 110 && wpm <= 160:
			r = 5
		case wpm >= 90 && wpm <= 180:
			r = 4
		case wpm >= 70 && wpm <= 200:
			r = 3
		default:
			r = 2
		}
	}
	per100 := float64(m.FillerWords) / float64(m.CandidateWords) * 100
	switch {
	case per100 >= 10:
		r -= 1.5
	case per100 >= 5:
		r -= 1
	case per100 >= 2:
		r -= 0.5
	}
	if m.LongPauses > 3 {
		r -= 0.5
	}
	return clamp(r, 1, 5)
}

func speechNotes(m domain.SpeechMetrics, lang string) []string {
	id := lang == "id"
	var n []string
	if m.WordsPerMinute > 0 {
		pace, paceID := "a comfortable pace", "tempo yang nyaman"
		switch {
		case m.WordsPerMinute < 90:
			pace, paceID = "a slow pace", "tempo yang lambat"
		case m.WordsPerMinute > 180:
			pace, paceID = "a fast pace", "tempo yang cepat"
		}
		if id {
			n = append(n, fmt.Sprintf("Berbicara sekitar %.0f kata per menit, %s.", m.WordsPerMinute, paceID))
		} else {
			n = append(n, fmt.Sprintf("Spoke at about %.0f words per minute, %s.", m.WordsPerMinute, pace))
		}
	}
	if m.CandidateWords > 0 {
		if id {
			n = append(n, fmt.Sprintf("Menggunakan %d kata pengisi (seperti \"eee\" atau \"hmm\") dari %d kata.", m.FillerWords, m.CandidateWords))
		} else {
			n = append(n, fmt.Sprintf("Used %d filler words (like \"eee\" or \"hmm\") across %d words.", m.FillerWords, m.CandidateWords))
		}
	}
	if m.LongPauses > 0 {
		if id {
			n = append(n, fmt.Sprintf("Butuh lebih dari 6 detik untuk mulai menjawab sebanyak %d kali.", m.LongPauses))
		} else {
			n = append(n, fmt.Sprintf("Took more than 6 seconds to start answering %d time(s).", m.LongPauses))
		}
	}
	return n
}

// ---------- Prompts ----------

func systemPrompt(lang string) string {
	return `You are an experienced, fair technical recruiter. You evaluate a first-round SCREENING interview that an AI interviewer held with a job candidate, mostly in Bahasa Indonesia (English technical terms are normal and fine).

Rate on a 1–5 scale (decimals allowed):
1 = no relevant answer or clearly wrong; 2 = weak, major gaps; 3 = acceptable for the level, some gaps; 4 = solid, minor gaps; 5 = excellent, complete and precise.

Rules:
- Judge only what the candidate actually said in the transcript. Never assume knowledge they did not show.
- Calibrate to the job level (a junior candidate is not expected to know senior topics).
- Never consider accent, dialect, grammar slips, gender, age, religion, ethnicity or anything unrelated to the job.
- Transcripts come from speech recognition and may contain recognition errors; be lenient about obvious mis-transcriptions.
- "notes" describe the answers factually and neutrally (about the answers, not the person's character), max 3 per dimension.
- "recruiterSummary": 2–4 frank sentences for the recruiter, including the main risk.
- "evidence": a short quote (max 25 words) from the candidate's own words, verbatim.
- Write "notes", "recruiterSummary", "reasoning", "coveredPoints" and "missedPoints" in ` + lang + `.
- "candidate" is the feedback shown to the candidate, written TWICE with the same meaning: "en" in natural English and "id" in natural, friendly Bahasa Indonesia (use "kamu"):
  - "summary": 2–3 sentences addressed to the candidate ("you"/"kamu"), honest but kind and encouraging.
  - "strengths" and "improvements": up to 4 short points each; improvements phrased constructively.
  - "tips": 3–5 specific, actionable, positive tips that would help this candidate do better next time.
  - "notes": for each dimension, up to 3 short, supportive notes about the answers, addressed to the candidate.

Reply with ONLY one JSON object, no markdown, in exactly this shape:
{"questions":[{"questionId":"<id from the list>","score":1-5,"coveredPoints":["..."],"missedPoints":["..."],"evidence":"...","reasoning":"..."}],
"ratings":{"technical":1-5,"problemSolving":1-5,"communication":1-5,"motivation":1-5},
"notes":{"technical":["..."],"problemSolving":["..."],"communication":["..."],"motivation":["..."]},
"recruiterSummary":"...",
"candidate":{"en":{"summary":"...","strengths":["..."],"improvements":["..."],"tips":["..."],"notes":{"technical":["..."],"problemSolving":["..."],"communication":["..."],"motivation":["..."]}},
"id":{"summary":"...","strengths":["..."],"improvements":["..."],"tips":["..."],"notes":{"technical":["..."],"problemSolving":["..."],"communication":["..."],"motivation":["..."]}}}}
Include only questions that were actually asked in the transcript.`
}

func userPrompt(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "JOB: %s (%s level, %s division)\n", in.Job.Title, in.Job.Level, in.Job.Division)
	if len(in.Job.Skills) > 0 {
		fmt.Fprintf(&b, "KEY SKILLS: %s\n", strings.Join(in.Job.Skills, ", "))
	}
	b.WriteString("\nQUESTION BANK (the interviewer picked from these):\n")
	for _, q := range in.Questions {
		fmt.Fprintf(&b, "- id=%s | skill=%s | difficulty=%d | type=%s\n  Q: %s\n  Expected points: %s\n",
			q.ID, q.Skill, q.Difficulty, q.Type, q.Text, strings.Join(q.ExpectedPoints, "; "))
	}
	b.WriteString("\nTRANSCRIPT:\n")
	total := 0
	for _, t := range in.Turns {
		who := "INTERVIEWER"
		if t.Speaker == "candidate" {
			who = "CANDIDATE"
		}
		tag := ""
		if t.QuestionID != "" {
			tag = " [question " + t.QuestionID + "]"
		}
		line := fmt.Sprintf("%s%s: %s\n", who, tag, t.Text)
		total += len(line)
		if total > 60000 {
			b.WriteString("[transcript truncated]\n")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// ---------- helpers ----------

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	return math.Max(lo, math.Min(hi, v))
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func trimList(in []string, n int) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, truncate(s, 400))
		}
		if len(out) == n {
			break
		}
	}
	return out
}
