package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
)

type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

func writeFieldErrors(w http.ResponseWriter, fields security.FieldErrors) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]apiError{"error": {
		Code: "validation", Message: "Please fix the highlighted fields.", Fields: fields,
	}})
}

// readJSON decodes a size-limited JSON body and rejects unknown fields,
// so typos and unexpected input fail loudly instead of being ignored.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Request body is too large.")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_json", "Request body is not valid JSON: "+trimJSONErr(err))
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "bad_json", "Request body must contain a single JSON object.")
		return false
	}
	return true
}

func trimJSONErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// clientIP returns the caller's IP. Behind our own Next.js proxy the real IP is
// the last X-Forwarded-For entry (the one the proxy appended), not the first,
// which the client could forge.
func (a *API) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// newID returns a random UUID v4.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}
