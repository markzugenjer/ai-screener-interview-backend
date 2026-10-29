package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/livekit"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/worker"
)

func (a *API) myInterviews(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.ListUserInterviews(r.Context(), userFrom(r).ID)
	if err != nil {
		a.log.Error("list interviews", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load your interviews.")
		return
	}
	lang := feedbackLang(r)
	out := make([]interviewDTO, 0, len(list))
	for i := range list {
		out = append(out, summaryDTO(&list[i], lang))
	}
	writeJSON(w, http.StatusOK, map[string]any{"interviews": out})
}

// loadOwnInterview returns the interview only if it belongs to the caller
// (or the caller is staff). Others get 404, so ids cannot be probed.
func (a *API) loadOwnInterview(w http.ResponseWriter, r *http.Request, ownerOnly bool) *domain.Interview {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Interview not found.")
		return nil
	}
	iv, err := a.store.GetInterview(r.Context(), id)
	u := userFrom(r)
	if err != nil || (iv.UserID != u.ID && (ownerOnly || !a.canRecruit(u))) {
		writeError(w, http.StatusNotFound, "not_found", "Interview not found.")
		return nil
	}
	return iv
}

func (a *API) startInterview(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	j, err := a.store.GetJobBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || j.Status != domain.JobOpen {
		writeError(w, http.StatusNotFound, "not_found", "This job is not open for interviews.")
		return
	}
	if j.QuestionCount == 0 {
		writeError(w, http.StatusConflict, "no_questions", "This interview is not ready yet. Please try again later.")
		return
	}
	// Resume an unfinished interview for the same job instead of creating a new one.
	list, err := a.store.ListUserInterviews(r.Context(), u.ID)
	if err == nil {
		for _, s := range list {
			open := s.Status == domain.InterviewPending || s.Status == domain.InterviewInProgress
			if s.JobID == j.ID && open && time.Since(s.CreatedAt) < 2*time.Hour {
				iv := s.Interview
				writeJSON(w, http.StatusOK, map[string]any{"interview": toInterviewDTO(&iv), "resumed": true})
				return
			}
		}
	}
	attempts, err := a.store.CountAttempts(r.Context(), u.ID, j.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not start the interview.")
		return
	}
	completed, _ := a.store.CountCompletedAttempts(r.Context(), u.ID, j.ID)
	id := newID()
	iv := &domain.Interview{ID: id, JobID: j.ID, UserID: u.ID, Status: domain.InterviewPending,
		Attempt: attempts + 1, IsPractice: completed > 0, RoomName: "interview-" + id, CreatedAt: time.Now().UTC()}
	if err := a.store.CreateInterview(r.Context(), iv); err != nil {
		a.log.Error("create interview", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not start the interview.")
		return
	}
	a.audit(r, "interview.create", "interview", id, map[string]any{"job": j.ID, "practice": iv.IsPractice})
	writeJSON(w, http.StatusCreated, map[string]any{"interview": toInterviewDTO(iv), "resumed": false})
}

func (a *API) getInterview(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, false)
	if iv == nil {
		return
	}
	dto := toInterviewDTO(iv)
	if j, err := a.store.GetJob(r.Context(), iv.JobID); err == nil {
		jt := j.Text(domain.NormalizeLanguage(r.URL.Query().Get("lang")))
		dto.Job = &jobBrief{Title: jt.Title, Slug: j.Slug, Category: jt.Category, Level: j.Level,
			Division: jt.Division, Skills: nonNil(j.Skills), DurationMinutes: j.DurationMinutes}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interview": dto})
}

// interviewToken returns a short-lived LiveKit token that lets the candidate's
// browser join this interview's room and dispatches the AI interviewer into it.
func (a *API) interviewToken(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, true)
	if iv == nil {
		return
	}
	if iv.Status != domain.InterviewPending && iv.Status != domain.InterviewInProgress {
		writeError(w, http.StatusConflict, "finished", "This interview has already ended.")
		return
	}
	if !a.cfg.LiveKitConfigured() {
		writeError(w, http.StatusServiceUnavailable, "voice_unavailable", "The voice service is not configured yet.")
		return
	}
	u := userFrom(r)
	meta, _ := json.Marshal(map[string]string{"interviewId": iv.ID})
	token, err := livekit.CreateToken(livekit.TokenParams{
		APIKey: a.cfg.LiveKitAPIKey, APISecret: a.cfg.LiveKitAPISecret,
		Identity: "candidate-" + u.ID, Name: firstName(u.FullName), Room: iv.RoomName,
		AgentName: a.cfg.LiveKitAgentName, AgentMetadata: string(meta), TTL: 2 * time.Hour,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not connect to the interview room.")
		return
	}
	if iv.Status == domain.InterviewPending {
		now := time.Now().UTC()
		iv.Status, iv.StartedAt = domain.InterviewInProgress, &now
		if err := a.store.UpdateInterview(r.Context(), iv); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "Could not start the interview.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url": a.cfg.LiveKitURL, "token": token, "roomName": iv.RoomName, "interview": toInterviewDTO(iv),
	})
}

// resetInterview restarts the session. Allowed once, and only within the first
// two minutes, so a reset cannot be used to "practice" the real questions.
func (a *API) resetInterview(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, true)
	if iv == nil {
		return
	}
	if !canReset(iv) {
		msg := "This interview can no longer be reset."
		if iv.ResetCount >= maxResets {
			msg = "You have already used your one reset for this interview."
		} else if iv.Status == domain.InterviewInProgress {
			msg = "Reset is only available during the first 2 minutes."
		}
		writeError(w, http.StatusConflict, "reset_not_allowed", msg)
		return
	}
	if err := a.store.ClearTurns(r.Context(), iv.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not reset the interview.")
		return
	}
	iv.ResetCount++
	iv.RoomName = fmt.Sprintf("interview-%s-r%d", iv.ID, iv.ResetCount)
	iv.Status, iv.StartedAt, iv.EndedAt = domain.InterviewPending, nil, nil
	if err := a.store.UpdateInterview(r.Context(), iv); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not reset the interview.")
		return
	}
	a.audit(r, "interview.reset", "interview", iv.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"interview": toInterviewDTO(iv)})
}

// endInterview marks the interview finished and queues scoring when the
// candidate actually answered something. Shared by the candidate's Leave button
// and the voice agent's completion call.
func (a *API) endInterview(r *http.Request, iv *domain.Interview) error {
	if iv.Status != domain.InterviewPending && iv.Status != domain.InterviewInProgress {
		return nil // already finished; idempotent
	}
	now := time.Now().UTC()
	iv.EndedAt = &now
	if iv.StartedAt != nil {
		iv.DurationSeconds = int(now.Sub(*iv.StartedAt).Seconds())
	}
	turns, err := a.store.ListTurns(r.Context(), iv.ID)
	if err != nil {
		return err
	}
	answered := false
	for _, t := range turns {
		if t.Speaker == "candidate" && len(strings.Fields(t.Text)) >= 3 {
			answered = true
			break
		}
	}
	if !answered {
		iv.Status = domain.InterviewAbandoned
		return a.store.UpdateInterview(r.Context(), iv)
	}
	iv.Status = domain.InterviewCompleted
	if err := a.store.UpdateInterview(r.Context(), iv); err != nil {
		return err
	}
	return worker.EnqueueScoring(r.Context(), a.store, iv.ID)
}

func (a *API) finishInterview(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, true)
	if iv == nil {
		return
	}
	if err := a.endInterview(r, iv); err != nil {
		a.log.Error("finish interview", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not finish the interview.")
		return
	}
	a.audit(r, "interview.finish", "interview", iv.ID, map[string]any{"status": iv.Status})
	writeJSON(w, http.StatusOK, map[string]any{"interview": toInterviewDTO(iv)})
}

type eventInput struct {
	Type string `json:"type"`
}

func (a *API) interviewEvent(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, true)
	if iv == nil {
		return
	}
	var in eventInput
	if !readJSON(w, r, &in) {
		return
	}
	if iv.Status != domain.InterviewInProgress {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	const capN = 500
	switch in.Type {
	case "tab_hidden":
		iv.Integrity.TabHidden = min(iv.Integrity.TabHidden+1, capN)
	case "window_blur":
		iv.Integrity.WindowBlur = min(iv.Integrity.WindowBlur+1, capN)
	case "paste":
		iv.Integrity.PasteEvents = min(iv.Integrity.PasteEvents+1, capN)
	default:
		writeError(w, http.StatusBadRequest, "bad_event", "Unknown event type.")
		return
	}
	_ = a.store.UpdateInterview(r.Context(), iv)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) feedback(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, true)
	if iv == nil {
		return
	}
	j, err := a.store.GetJob(r.Context(), iv.JobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Interview not found.")
		return
	}
	dto := toInterviewDTO(iv)
	jt := j.Text(feedbackLang(r))
	dto.Job = &jobBrief{Title: jt.Title, Slug: j.Slug, Category: jt.Category, Level: j.Level,
		Division: jt.Division, Skills: nonNil(j.Skills), DurationMinutes: j.DurationMinutes}
	resp := map[string]any{"interview": dto}
	sc, err := a.store.GetScore(r.Context(), iv.ID)
	switch {
	case err == nil:
		resp["feedback"] = toCandidateScore(sc, feedbackLang(r))
	case errors.Is(err, store.ErrNotFound):
		resp["feedback"] = nil
	default:
		writeError(w, http.StatusInternalServerError, "internal", "Could not load feedback.")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func firstName(full string) string {
	if f := strings.Fields(full); len(f) > 0 {
		return f[0]
	}
	return "Candidate"
}

// scoreNow is called by the feedback page while an interview waits for its score.
// With inline jobs it scores right here (the page shows "scoring…" meanwhile);
// otherwise the background worker does it and this just reports the status.
func (a *API) scoreNow(w http.ResponseWriter, r *http.Request) {
	iv := a.loadOwnInterview(w, r, true)
	if iv == nil {
		return
	}
	if iv.Status == domain.InterviewCompleted {
		a.runJobs(r)
		if fresh, err := a.store.GetInterview(r.Context(), iv.ID); err == nil {
			iv = fresh
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interview": toInterviewDTO(iv)})
}
