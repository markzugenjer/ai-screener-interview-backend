// Package translate turns a job posting's public text into another language
// with the model, so recruiters write a job once and the job board can follow
// each visitor's language. Recruiters can always review and edit the result.
package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/llm"
)

var languageNames = map[string]string{"id": "Bahasa Indonesia", "en": "English"}

// Supported reports whether lang is a job language.
func Supported(lang string) bool { _, ok := languageNames[lang]; return ok }

const system = `You translate job postings for FDC Dental Clinic, a dental clinic chain in Indonesia.
Translate from %s to %s.
- Keep the meaning, a professional and friendly tone, and the same number of requirements in the same order.
- Use natural wording a local job seeker would expect; common job titles may stay as they are when that is usual in the target language.
- Keep names unchanged: "FDC Dental Clinic", cities, brands, product and tool names.
- Keep placeholders, numbers and markup exactly as they are (for example [5], 2 tahun -> 2 years, line breaks and "- " bullets).
- Translate every field, including short labels such as division, category and employment type.
Reply with ONLY one JSON object with exactly these keys:
{"title":"","division":"","category":"","location":"","employmentType":"","description":"","requirements":[""]}`

// Job translates t from one job language to another.
func Job(ctx context.Context, c llm.Completer, from, to string, t domain.JobText) (domain.JobText, error) {
	if !Supported(from) || !Supported(to) || from == to {
		return domain.JobText{}, fmt.Errorf("unsupported translation %s -> %s", from, to)
	}
	in, _ := json.Marshal(t)
	reply, err := c.Complete(ctx, fmt.Sprintf(system, languageNames[from], languageNames[to]), string(in), 4096)
	if err != nil {
		return domain.JobText{}, err
	}
	raw, err := llm.ExtractJSON(reply)
	if err != nil {
		return domain.JobText{}, err
	}
	var out domain.JobText
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return domain.JobText{}, fmt.Errorf("parse translation: %w", err)
	}
	out = clean(out)
	if strings.TrimSpace(t.Title) != "" && out.Title == "" {
		return domain.JobText{}, errors.New("translation is missing the title")
	}
	if len(t.Requirements) > 0 && len(out.Requirements) == 0 {
		return domain.JobText{}, errors.New("translation is missing the requirements")
	}
	return out, nil
}

// clean trims fields and keeps them within the same limits as recruiter input.
func clean(t domain.JobText) domain.JobText {
	cut := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if r := []rune(s); len(r) > n {
			return string(r[:n])
		}
		return s
	}
	out := domain.JobText{
		Title: cut(t.Title, 120), Division: cut(t.Division, 60), Category: cut(t.Category, 40),
		Location: cut(t.Location, 80), EmploymentType: cut(t.EmploymentType, 40), Description: cut(t.Description, 6000),
		Requirements: []string{},
	}
	for _, r := range t.Requirements {
		if r = cut(r, 300); r != "" && len(out.Requirements) < 20 {
			out.Requirements = append(out.Requirements, r)
		}
	}
	return out
}
