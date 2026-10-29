package api

import (
	"net/http"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
)

// Response shapes. Internal fields (password hashes, encrypted values, storage
// keys) never leave the server; these structs are the only thing serialized.

type userDTO struct {
	ID         string    `json:"id"`
	FullName   string    `json:"fullName"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	WhatsApp   string    `json:"whatsapp"` // masked
	ResumeName string    `json:"resumeName"`
	CreatedAt  time.Time `json:"createdAt"`
	CanRecruit bool      `json:"canRecruit"` // may use the recruiter area
	ViaJobi    bool      `json:"viaJobi"`    // signs in with JOBI (no password)
}

func (a *API) toUserDTO(u *domain.User) userDTO {
	phone := ""
	if u.PhoneEnc != "" {
		if p, err := a.enc.DecryptString(u.PhoneEnc, "phone:"+u.ID); err == nil {
			phone = security.MaskPhone(p)
		}
	}
	return userDTO{ID: u.ID, FullName: u.FullName, Email: u.Email, Role: string(u.Role), WhatsApp: phone,
		ResumeName: u.ResumeName, CreatedAt: u.CreatedAt, CanRecruit: a.canRecruit(u), ViaJobi: u.JobiSub != ""}
}

type jobDTO struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	Slug            string          `json:"slug"`
	Division        string          `json:"division"`
	Category        string          `json:"category"`
	Level           string          `json:"level"`
	Location        string          `json:"location"`
	EmploymentType  string          `json:"employmentType"`
	Description     string          `json:"description"`
	Requirements    []string        `json:"requirements"`
	Skills          []string        `json:"skills"`
	DurationMinutes int             `json:"durationMinutes"`
	Status          string          `json:"status"`
	QuestionCount   int             `json:"questionCount"`
	Weights         *domain.Weights `json:"weights,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	// Lang is the language the text fields are in; SourceLang and Translations
	// are only sent to the job editor.
	Lang         string                    `json:"lang"`
	SourceLang   string                    `json:"sourceLang,omitempty"`
	Translations map[string]domain.JobText `json:"translations,omitempty"`
}

// jobView says who sees a job and in which language: Lang "" means the source
// language; Edit sends the original plus every translation (job editor).
type jobView struct {
	Staff bool
	Edit  bool
	Lang  string
}

func toJobDTO(j *domain.Job, v jobView) jobDTO {
	lang := v.Lang
	if lang == "" || v.Edit {
		lang = j.SourceLang
	}
	if lang == "" {
		lang = "id"
	}
	t := j.Text(lang)
	d := jobDTO{ID: j.ID, Title: t.Title, Slug: j.Slug, Division: t.Division, Category: t.Category,
		Level: j.Level, Location: t.Location, EmploymentType: t.EmploymentType, Description: t.Description,
		Requirements: nonNil(t.Requirements), Skills: nonNil(j.Skills), DurationMinutes: j.DurationMinutes,
		Status: string(j.Status), QuestionCount: j.QuestionCount, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
		Lang: lang}
	if v.Staff {
		w := j.Weights
		d.Weights = &w
	}
	if v.Edit {
		d.SourceLang = j.SourceLang
		d.Translations = map[string]domain.JobText{}
		for k, tr := range j.Translations {
			tr.Requirements = nonNil(tr.Requirements)
			d.Translations[k] = tr
		}
	}
	return d
}

type interviewDTO struct {
	ID              string     `json:"id"`
	JobID           string     `json:"jobId"`
	Status          string     `json:"status"`
	Attempt         int        `json:"attempt"`
	IsPractice      bool       `json:"isPractice"`
	ResetCount      int        `json:"resetCount"`
	CanReset        bool       `json:"canReset"`
	StartedAt       *time.Time `json:"startedAt"`
	EndedAt         *time.Time `json:"endedAt"`
	DurationSeconds int        `json:"durationSeconds"`
	CreatedAt       time.Time  `json:"createdAt"`
	Job             *jobBrief  `json:"job,omitempty"`
	Overall         *int       `json:"overall,omitempty"`
	Summary         string     `json:"summary,omitempty"`
}

type jobBrief struct {
	Title           string   `json:"title"`
	Slug            string   `json:"slug"`
	Category        string   `json:"category"`
	Level           string   `json:"level"`
	Division        string   `json:"division"`
	Skills          []string `json:"skills"`
	DurationMinutes int      `json:"durationMinutes,omitempty"`
}

const (
	resetWindow = 2 * time.Minute
	maxResets   = 1
)

func canReset(iv *domain.Interview) bool {
	if iv.ResetCount >= maxResets {
		return false
	}
	switch iv.Status {
	case domain.InterviewPending:
		return true
	case domain.InterviewInProgress:
		return iv.StartedAt == nil || time.Since(*iv.StartedAt) <= resetWindow
	}
	return false
}

func toInterviewDTO(iv *domain.Interview) interviewDTO {
	return interviewDTO{ID: iv.ID, JobID: iv.JobID, Status: string(iv.Status), Attempt: iv.Attempt,
		IsPractice: iv.IsPractice, ResetCount: iv.ResetCount, CanReset: canReset(iv), StartedAt: iv.StartedAt,
		EndedAt: iv.EndedAt, DurationSeconds: iv.DurationSeconds, CreatedAt: iv.CreatedAt}
}

func summaryDTO(s *domain.InterviewSummary, lang string) interviewDTO {
	d := toInterviewDTO(&s.Interview)
	jt := s.JobText(lang)
	d.Job = &jobBrief{Title: jt.Title, Slug: s.JobSlug, Category: jt.Category, Level: s.JobLevel,
		Division: jt.Division, Skills: nonNil(s.JobSkills)}
	d.Overall = s.Overall
	d.Summary = s.Summary
	if v := s.SummaryByLang[lang]; v != "" {
		d.Summary = v
	}
	return d
}

// feedbackLang reads ?lang= (en, id), defaulting to English.
func feedbackLang(r *http.Request) string {
	if l := domain.NormalizeLanguage(r.URL.Query().Get("lang")); l != "" {
		return l
	}
	return "en"
}

type resultRowDTO struct {
	interviewDTO
	CandidateName  string   `json:"candidateName"`
	CandidateEmail string   `json:"candidateEmail"`
	Recommendation string   `json:"recommendation"`
	Technical      *float64 `json:"technical"`
	Communication  *float64 `json:"communication"`
	Decision       string   `json:"decision"`
}

// candidateScoreDTO is what candidates see: encouraging summary, breakdown and
// tips. Recruiter-only data (recommendation, frank summary, per-question
// evidence and the question bank's expected answers) is deliberately left out.
type candidateScoreDTO struct {
	Overall    int                `json:"overall"`
	Summary    string             `json:"summary"`
	Dimensions []domain.Dimension `json:"dimensions"`
	Strengths  []string           `json:"strengths"`
	Growth     []string           `json:"growth"`
	Tips       []string           `json:"tips"`
	ScoredAt   time.Time          `json:"scoredAt"`
}

func toCandidateScore(s *domain.Score, lang string) candidateScoreDTO {
	ct := s.CandidateFor(lang)
	dims := make([]domain.Dimension, len(s.Dimensions))
	for i, d := range s.Dimensions {
		if n, ok := ct.Notes[d.Key]; ok {
			d.Notes = nonNil(n)
		}
		dims[i] = d
	}
	return candidateScoreDTO{Overall: s.Overall, Summary: ct.Summary, Dimensions: dims,
		Strengths: nonNil(ct.Strengths), Growth: nonNil(ct.Growth), Tips: nonNil(ct.Tips), ScoredAt: s.CreatedAt}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
