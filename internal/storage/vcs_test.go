package storage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
)

// fakeVCS mimics the signer plus the bucket: grants point back at itself.
func fakeVCS(t *testing.T) (*httptest.Server, map[string][]byte) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/sign" {
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			key := req["key"].(string)
			if !strings.HasPrefix(key, "files/") {
				t.Errorf("key without prefix: %s", key)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"url": srv.URL + "/obj/" + key, "headers": map[string]string{"X-Signed": "1"}})
			return
		}
		if r.Header.Get("X-Signed") != "1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		k := strings.TrimPrefix(r.URL.Path, "/obj/")
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			objects[k] = b
		case http.MethodGet:
			b, ok := objects[k]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodDelete:
			delete(objects, k)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, objects
}

func TestVCSRoundTripEncrypted(t *testing.T) {
	srv, objects := fakeVCS(t)
	enc, _ := security.NewEncryptor(make([]byte, 32))
	v, err := NewVCS(srv.URL+"/sign", "tok", enc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key, err := v.Put(ctx, []byte("%PDF-1.7 resume"), ".pdf", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	stored := objects["files/"+key]
	if len(stored) == 0 || strings.Contains(string(stored), "resume") {
		t.Fatal("bucket must hold ciphertext only")
	}
	got, err := v.Get(ctx, key, "user-1")
	if err != nil || string(got) != "%PDF-1.7 resume" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if _, err := v.Get(ctx, key, "someone-else"); err == nil {
		t.Fatal("other owner must not decrypt")
	}
	if err := v.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, key, "user-1"); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	if _, err := v.Get(ctx, "../etc/passwd", "user-1"); err != ErrBadKey {
		t.Fatal("bad key must be rejected")
	}
}
