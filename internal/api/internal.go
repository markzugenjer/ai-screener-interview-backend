package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
)

// These endpoints are called only by the voice agent, authenticated with
// INTERNAL_API_SECRET. Never expose /internal/* through the public proxy.

func (a *API) internalInterview(w http.ResponseWriter, r *http.Request) *domain.Interview {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Interview not found.")
		return nil
	}
	iv, err := a.store.GetInterview(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Interview not found.")
		return nil
	}
	return iv
}

func (a *API) internalContext(w http.ResponseWriter, r *http.Request) {
	iv := a.internalInterview(w, r)
	if iv == nil {
		return
	}
	j, err := a.store.GetJob(r.Context(), iv.JobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Job not found.")
		return
	}
	qs, err := a.store.ListQuestions(r.Context(), iv.JobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Could not load questions.")
		return
	}
	u, err := a.store.GetUserByID(r.Context(), iv.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Candidate not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"interview": map[string]any{"id": iv.ID, "status": iv.Status, "roomName": iv.RoomName, "isPractice": iv.IsPractice},
		"job": map[string]any{"title": j.Title, "level": j.Level, "division": j.Division, "skills": nonNil(j.Skills),
			"description": j.Description, "durationMinutes": j.DurationMinutes},
		"candidate": map[string]any{"firstName": firstName(u.FullName)},
		"questions": nonNil(qs),
		"language":  "id-ID",
	})
}

type turnsInput struct {
	Turns []domain.Turn `json:"turns"`
}

func (a *API) internalTurns(w http.ResponseWriter, r *http.Request) {
	iv := a.internalInterview(w, r)
	if iv == nil {
		return
	}
	var in turnsInput
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Turns) == 0 || len(in.Turns) > 50 {
		writeError(w, http.StatusBadRequest, "bad_turns", "Send between 1 and 50 turns.")
		return
	}
	if iv.Status != domain.InterviewPending && iv.Status != domain.InterviewInProgress {
		writeError(w, http.StatusConflict, "finished", "This interview has already ended.")
		return
	}
	clean := make([]domain.Turn, 0, len(in.Turns))
	for _, t := range in.Turns {
		text := strings.TrimSpace(t.Text)
		if (t.Speaker != "ai" && t.Speaker != "candidate") || text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > 4000 {
			text = string([]rune(text)[:4000])
		}
		if len(t.QuestionID) > 64 {
			t.QuestionID = ""
		}
		clean = append(clean, domain.Turn{Speaker: t.Speaker, Text: text, StartMs: max(t.StartMs, 0),
			EndMs: max(t.EndMs, 0), QuestionID: t.QuestionID})
	}
	if err := a.store.AppendTurns(r.Context(), iv.ID, clean); err != nil {
		a.log.Error("append turns", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not save transcript.")
		return
	}
	if iv.Status == domain.InterviewPending {
		now := time.Now().UTC()
		iv.Status, iv.StartedAt = domain.InterviewInProgress, &now
		_ = a.store.UpdateInterview(r.Context(), iv)
	}
	writeJSON(w, http.StatusOK, map[string]int{"saved": len(clean)})
}

func (a *API) internalComplete(w http.ResponseWriter, r *http.Request) {
	iv := a.internalInterview(w, r)
	if iv == nil {
		return
	}
	if err := a.endInterview(r, iv); err != nil {
		a.log.Error("complete interview", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not complete the interview.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": iv.Status})
}
