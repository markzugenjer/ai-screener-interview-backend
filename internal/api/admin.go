package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/worker"
)

func (a *API) adminStats(w http.ResponseWriter, r *http.Request) {
	st, err := a.store.Stats(r.Context())
	if err != nil {
		a.log.Error("stats", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load stats.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": st})
}

func (a *API) adminResults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	minOverall, _ := strconv.Atoi(q.Get("minOverall"))
	minTech, _ := strconv.ParseFloat(q.Get("minTechnical"), 64)
	f := domain.ResultFilter{
		Status: q.Get("status"), Recommendation: q.Get("recommendation"), Decision: q.Get("decision"),
		MinOverall: min(max(minOverall, 0), 100), MinTechnical: min(max(minTech, 0), 5),
		Query: truncateStr(strings.TrimSpace(q.Get("q")), 80), IncludePractice: q.Get("practice") == "true",
		Sort: q.Get("sort"), Limit: size, Offset: (page - 1) * size,
	}
	if id := q.Get("jobId"); id != "" {
		if !validUUID(id) {
			writeError(w, http.StatusBadRequest, "bad_filter", "Invalid job filter.")
			return
		}
		f.JobID = id
	}
	statuses := []string{"", "in_progress", "completed", "scored", "failed", "abandoned"}
	recs := []string{"", "strong_yes", "yes", "maybe", "no"}
	decisions := []string{"", "none", "shortlist", "reject", "hold"}
	sorts := []string{"", "-date", "date", "-overall", "overall"}
	if !slices.Contains(statuses, f.Status) || !slices.Contains(recs, f.Recommendation) ||
		!slices.Contains(decisions, f.Decision) || !slices.Contains(sorts, f.Sort) {
		writeError(w, http.StatusBadRequest, "bad_filter", "Invalid filter value.")
		return
	}
	rows, total, err := a.store.ListResults(r.Context(), f)
	if err != nil {
		a.log.Error("list results", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load results.")
		return
	}
	out := make([]resultRowDTO, 0, len(rows))
	for i := range rows {
		s := &rows[i]
		out = append(out, resultRowDTO{interviewDTO: summaryDTO(s), CandidateName: s.CandidateName,
			CandidateEmail: s.CandidateEmail, Recommendation: s.Recommendation, Technical: s.Technical,
			Communication: s.Communication, Decision: s.Decision})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out, "total": total, "page": page, "pageSize": size})
}

func (a *API) adminResult(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, false)
	if iv == nil {
		return
	}
	ctx := r.Context()
	j, err := a.store.GetJob(ctx, iv.JobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Interview not found.")
		return
	}
	cand, err := a.store.GetUserByID(ctx, iv.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Candidate not found.")
		return
	}
	phone := ""
	if cand.PhoneEnc != "" {
		phone, _ = a.enc.DecryptString(cand.PhoneEnc, "phone:"+cand.ID)
	}
	turns, _ := a.store.ListTurns(ctx, iv.ID)
	reviews, _ := a.store.ListReviews(ctx, iv.ID)
	var score any
	if sc, err := a.store.GetScore(ctx, iv.ID); err == nil {
		score = sc
	}
	a.audit(r, "result.view", "interview", iv.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"interview": toInterviewDTO(iv),
		"integrity": iv.Integrity,
		"lastError": iv.LastError,
		"job":       toJobDTO(j, true),
		"candidate": map[string]any{
			"id": cand.ID, "fullName": cand.FullName, "email": cand.Email, "whatsapp": phone,
			"resumeName": cand.ResumeName, "hasResume": cand.ResumeKey != "", "createdAt": cand.CreatedAt,
		},
		"score":      score,
		"transcript": nonNil(turns),
		"reviews":    nonNil(reviews),
	})
}

type reviewInput struct {
	Decision string `json:"decision"`
	Notes    string `json:"notes"`
}

func (a *API) adminReview(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, false)
	if iv == nil {
		return
	}
	var in reviewInput
	if !readJSON(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"shortlist", "reject", "hold"}, in.Decision) {
		writeError(w, http.StatusUnprocessableEntity, "validation", "Decision must be shortlist, reject or hold.")
		return
	}
	notes := strings.TrimSpace(in.Notes)
	if runes(notes) > 2000 {
		writeError(w, http.StatusUnprocessableEntity, "validation", "Notes must be at most 2000 characters.")
		return
	}
	u := userFrom(r)
	rv := &domain.Review{ID: newID(), InterviewID: iv.ID, ReviewerID: u.ID, Reviewer: u.FullName,
		Decision: in.Decision, Notes: notes, CreatedAt: time.Now().UTC()}
	if err := a.store.AddReview(r.Context(), rv); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not save the review.")
		return
	}
	a.audit(r, "result.review", "interview", iv.ID, map[string]any{"decision": in.Decision})
	writeJSON(w, http.StatusCreated, map[string]any{"review": rv})
}

func (a *API) adminRescore(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, false)
	if iv == nil {
		return
	}
	if iv.Status != domain.InterviewCompleted && iv.Status != domain.InterviewFailed && iv.Status != domain.InterviewScored {
		writeError(w, http.StatusConflict, "not_finished", "Only finished interviews can be re-scored.")
		return
	}
	iv.Status, iv.LastError = domain.InterviewCompleted, ""
	if err := a.store.UpdateInterview(r.Context(), iv); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not queue scoring.")
		return
	}
	if err := worker.EnqueueScoring(r.Context(), a.store, iv.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not queue scoring.")
		return
	}
	a.audit(r, "result.rescore", "interview", iv.ID, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"interview": toInterviewDTO(iv)})
}

// adminResume decrypts and streams a candidate's resume. Every download is audited.
func (a *API) adminResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Resume not found.")
		return
	}
	u, err := a.store.GetUserByID(r.Context(), id)
	if err != nil || u.ResumeKey == "" {
		writeError(w, http.StatusNotFound, "not_found", "Resume not found.")
		return
	}
	data, err := a.files.Get(r.Context(), u.ResumeKey, u.ID)
	if err != nil {
		a.log.Error("read resume", "err", err)
		writeError(w, http.StatusNotFound, "not_found", "Resume not found.")
		return
	}
	a.audit(r, "resume.download", "user", u.ID, nil)
	name := strings.ReplaceAll(u.ResumeName, `"`, "")
	w.Header().Set("Content-Type", u.ResumeMime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
