package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/storage"
)

type diagStep struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Code   string `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

func diag(step string, err error) diagStep {
	d := diagStep{Step: step, OK: err == nil}
	var se *storage.StorageError
	switch {
	case err == nil:
	case errors.As(err, &se):
		d.Step, d.Status, d.Code = se.Step, se.Status, se.Code
		if se.Err != nil {
			d.Error = se.Err.Error()
		}
	default:
		d.Error = "failed"
	}
	return d
}

// adminStorageCheck uploads, reads back and deletes a tiny encrypted test file,
// so a storage problem shows which step fails (admin only; never shows signed
// URLs, tokens or bucket details).
func (a *API) adminStorageCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	backend := "local"
	if _, ok := a.files.(*storage.VCS); ok {
		backend = "vcs"
	}
	payload := []byte("dicy-talks storage self-test")
	steps := []diagStep{}
	key, err := a.files.Put(ctx, payload, ".bin", "storage-self-test")
	steps = append(steps, diag("upload", err))
	if err == nil {
		got, err := a.files.Get(ctx, key, "storage-self-test")
		if err == nil && !bytes.Equal(got, payload) {
			err = errors.New("content mismatch")
		}
		steps = append(steps, diag("download", err))
		steps = append(steps, diag("delete", a.files.Delete(ctx, key)))
	}
	ok := true
	for _, s := range steps {
		ok = ok && s.OK
	}
	a.audit(r, "storage.check", "storage", backend, map[string]any{"ok": ok})
	writeJSON(w, http.StatusOK, map[string]any{"backend": backend, "ok": ok, "steps": steps})
}
