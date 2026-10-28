package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

func (a *API) listOpenJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	jobs, err := a.store.ListJobs(r.Context(), store.JobFilter{
		Status: string(domain.JobOpen), Division: truncateStr(q.Get("division"), 60), Query: truncateStr(q.Get("q"), 80),
	})
	if err != nil {
		a.log.Error("list jobs", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load jobs.")
		return
	}
	out := make([]jobDTO, 0, len(jobs))
	for i := range jobs {
		out = append(out, toJobDTO(&jobs[i], false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (a *API) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := a.store.GetJobBySlug(r.Context(), r.PathValue("slug"))
	u := userFrom(r)
	staff := u != nil && u.Role.IsStaff()
	if err != nil || (j.Status != domain.JobOpen && !staff) {
		writeError(w, http.StatusNotFound, "not_found", "This job is not available.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": toJobDTO(j, staff)})
}

// ---------- Recruiter ----------

type questionInput struct {
	ID             string   `json:"id"`
	Skill          string   `json:"skill"`
	Difficulty     int      `json:"difficulty"`
	Type           string   `json:"type"`
	Text           string   `json:"text"`
	ExpectedPoints []string `json:"expectedPoints"`
	FollowUps      []string `json:"followUps"`
}

type jobInput struct {
	Title           string          `json:"title"`
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
	Weights         *domain.Weights `json:"weights"`
	Questions       []questionInput `json:"questions"`
}

var skillTagRe = regexp.MustCompile(`^[a-z0-9.+#\-]{1,30}$`)

func runes(s string) int { return utf8.RuneCountInString(s) }

func cleanList(in []string, max, maxLen int) ([]string, bool) {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if runes(s) > maxLen {
			return nil, false
		}
		out = append(out, s)
	}
	return out, len(out) <= max
}

func (in *jobInput) validate() (*domain.Job, []domain.Question, security.FieldErrors) {
	fe := security.FieldErrors{}
	j := &domain.Job{
		Title: strings.TrimSpace(in.Title), Division: strings.TrimSpace(in.Division),
		Category: strings.TrimSpace(in.Category), Level: in.Level, Location: strings.TrimSpace(in.Location),
		EmploymentType: strings.TrimSpace(in.EmploymentType), Description: strings.TrimSpace(in.Description),
		DurationMinutes: in.DurationMinutes, Status: domain.JobStatus(in.Status),
	}
	if n := runes(j.Title); n < 3 || n > 120 {
		fe["title"] = "Title must be 3–120 characters."
	}
	if j.Division == "" {
		j.Division = "Engineering"
	}
	if j.Category == "" {
		j.Category = "Technical"
	}
	if j.EmploymentType == "" {
		j.EmploymentType = "Full-time"
	}
	if runes(j.Division) > 60 || runes(j.Category) > 40 || runes(j.Location) > 80 || runes(j.EmploymentType) > 40 {
		fe["division"] = "Division, category, location and employment type must be short."
	}
	if !slices.Contains([]string{"junior", "mid", "senior"}, j.Level) {
		fe["level"] = "Level must be junior, mid or senior."
	}
	if runes(j.Description) > 6000 {
		fe["description"] = "Description must be at most 6000 characters."
	}
	var ok bool
	if j.Requirements, ok = cleanList(in.Requirements, 20, 300); !ok {
		fe["requirements"] = "Up to 20 requirements, each at most 300 characters."
	}
	j.Skills = []string{}
	for _, s := range in.Skills {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if !skillTagRe.MatchString(s) {
			fe["skills"] = "Skill tags use lowercase letters, numbers and . + # - only (e.g. react, node.js)."
			break
		}
		if !slices.Contains(j.Skills, s) {
			j.Skills = append(j.Skills, s)
		}
	}
	if len(j.Skills) > 12 {
		fe["skills"] = "Use at most 12 skill tags."
	}
	if j.DurationMinutes == 0 {
		j.DurationMinutes = 20
	}
	if j.DurationMinutes < 5 || j.DurationMinutes > 60 {
		fe["durationMinutes"] = "Duration must be 5–60 minutes."
	}
	if j.Status == "" {
		j.Status = domain.JobDraft
	}
	if !slices.Contains([]domain.JobStatus{domain.JobDraft, domain.JobOpen, domain.JobClosed}, j.Status) {
		fe["status"] = "Status must be draft, open or closed."
	}
	j.Weights = domain.DefaultWeights()
	if in.Weights != nil {
		w := *in.Weights
		for _, v := range []int{w.Technical, w.ProblemSolving, w.Communication, w.Speech, w.Motivation} {
			if v < 0 || v > 100 {
				fe["weights"] = "Each weight must be between 0 and 100."
			}
		}
		if w.Sum() != 100 {
			fe["weights"] = fmt.Sprintf("Weights must add up to 100 (now %d).", w.Sum())
		}
		j.Weights = w
	}

	var qs []domain.Question
	if len(in.Questions) > 100 {
		fe["questions"] = "Use at most 100 questions."
	}
	for i, q := range in.Questions {
		key := fmt.Sprintf("questions.%d", i)
		text := strings.TrimSpace(q.Text)
		if n := runes(text); n < 5 || n > 1000 {
			fe[key] = "Question text must be 5–1000 characters."
			continue
		}
		if q.Difficulty < 1 || q.Difficulty > 3 {
			fe[key] = "Difficulty must be 1, 2 or 3."
			continue
		}
		if q.Type == "" {
			q.Type = "technical"
		}
		if !slices.Contains([]string{"technical", "scenario", "soft"}, q.Type) {
			fe[key] = "Type must be technical, scenario or soft."
			continue
		}
		skill := strings.TrimSpace(q.Skill)
		if skill == "" || runes(skill) > 40 {
			fe[key] = "Each question needs a skill (max 40 characters)."
			continue
		}
		ep, ok1 := cleanList(q.ExpectedPoints, 10, 300)
		fu, ok2 := cleanList(q.FollowUps, 5, 300)
		if !ok1 || !ok2 {
			fe[key] = "Up to 10 expected points and 5 follow-ups, each at most 300 characters."
			continue
		}
		id := q.ID
		if !validUUID(id) {
			id = newID()
		}
		qs = append(qs, domain.Question{ID: id, Skill: skill, Difficulty: q.Difficulty, Type: q.Type, Text: text,
			ExpectedPoints: ep, FollowUps: fu, Sort: i})
	}
	if j.Status == domain.JobOpen && len(qs) < 3 && !fe.Any() {
		fe["questions"] = "An open job needs at least 3 questions."
	}
	return j, qs, fe
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "job"
	}
	return s
}

func (a *API) uniqueSlug(r *http.Request, title, exceptID string) string {
	base := slugify(title)
	slug := base
	for i := 2; i < 50; i++ {
		existing, err := a.store.GetJobBySlug(r.Context(), slug)
		if errors.Is(err, store.ErrNotFound) || (err == nil && existing.ID == exceptID) {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return base + "-" + newID()[:8]
}

func (a *API) adminListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	jobs, err := a.store.ListJobs(r.Context(), store.JobFilter{Status: q.Get("status"), Query: truncateStr(q.Get("q"), 80)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not load jobs.")
		return
	}
	out := make([]jobDTO, 0, len(jobs))
	for i := range jobs {
		out = append(out, toJobDTO(&jobs[i], true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (a *API) adminGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Job not found.")
		return
	}
	j, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Job not found.")
		return
	}
	qs, err := a.store.ListQuestions(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not load questions.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": toJobDTO(j, true), "questions": nonNil(qs)})
}

func (a *API) adminCreateJob(w http.ResponseWriter, r *http.Request) {
	var in jobInput
	if !readJSON(w, r, &in) {
		return
	}
	j, qs, fe := in.validate()
	if fe.Any() {
		writeFieldErrors(w, fe)
		return
	}
	j.ID = newID()
	j.Slug = a.uniqueSlug(r, j.Title, "")
	j.CreatedBy = userFrom(r).ID
	j.CreatedAt = time.Now().UTC()
	j.UpdatedAt = j.CreatedAt
	if err := a.store.CreateJob(r.Context(), j); err != nil {
		a.log.Error("create job", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not create the job.")
		return
	}
	if err := a.store.ReplaceQuestions(r.Context(), j.ID, qs); err != nil {
		a.log.Error("save questions", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Job created, but saving questions failed.")
		return
	}
	j.QuestionCount = len(qs)
	a.audit(r, "job.create", "job", j.ID, map[string]any{"title": j.Title})
	writeJSON(w, http.StatusCreated, map[string]any{"job": toJobDTO(j, true)})
}

func (a *API) adminUpdateJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.store.GetJob(r.Context(), id)
	if !validUUID(id) || err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Job not found.")
		return
	}
	var in jobInput
	if !readJSON(w, r, &in) {
		return
	}
	j, qs, fe := in.validate()
	if fe.Any() {
		writeFieldErrors(w, fe)
		return
	}
	j.ID = id
	j.Slug = existing.Slug
	if j.Title != existing.Title {
		j.Slug = a.uniqueSlug(r, j.Title, id)
	}
	j.CreatedBy, j.CreatedAt, j.UpdatedAt = existing.CreatedBy, existing.CreatedAt, time.Now().UTC()
	if err := a.store.UpdateJob(r.Context(), j); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not update the job.")
		return
	}
	if err := a.store.ReplaceQuestions(r.Context(), id, qs); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not save questions.")
		return
	}
	j.QuestionCount = len(qs)
	a.audit(r, "job.update", "job", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"job": toJobDTO(j, true)})
}

func (a *API) adminDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Job not found.")
		return
	}
	if err := a.store.DeleteJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Job not found.")
		return
	}
	a.audit(r, "job.delete", "job", id, nil)
	w.WriteHeader(http.StatusNoContent)
}
