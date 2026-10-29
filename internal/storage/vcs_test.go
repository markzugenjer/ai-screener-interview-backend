package storage

import (
	"context"
	"encoding/json"
	"errors"
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
	return fakeVCSWith(t, false)
}

// fakeVCSWith: wrapped=true answers {"grant": {...}} and lists no headers, the way
// a signer may; the bucket still insists on the signed Content-Type for uploads.
func fakeVCSWith(t *testing.T, wrapped bool) (*httptest.Server, map[string][]byte) {
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
			if req["action"] == "upload" && req["content_type"] != "application/octet-stream" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_content_type"}}`))
				return
			}
			grant := map[string]any{"url": srv.URL + "/obj/" + key + "?sig=1", "headers": map[string][]string{"X-Signed": {"1"}}}
			if wrapped {
				grant = map[string]any{"grant": map[string]any{"url": srv.URL + "/obj/" + key + "?sig=1", "method": "put"}}
			}
			if req["action"] != "upload" && wrapped {
				grant = map[string]any{"grant": map[string]any{"url": srv.URL + "/obj/" + key + "?sig=1"}}
			}
			_ = json.NewEncoder(w).Encode(grant)
			return
		}
		if r.URL.Query().Get("sig") != "1" || (!wrapped && r.Header.Get("X-Signed") != "1") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPut && r.Header.Get("Content-Type") != "application/octet-stream" {
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

func TestVCSWrappedGrantAndSignedContentType(t *testing.T) {
	srv, _ := fakeVCSWith(t, true)
	enc, _ := security.NewEncryptor(make([]byte, 32))
	v, _ := NewVCS(srv.URL+"/sign", "tok", enc)
	ctx := context.Background()
	key, err := v.Put(ctx, []byte("resume"), ".pdf", "u")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if got, err := v.Get(ctx, key, "u"); err != nil || string(got) != "resume" {
		t.Fatalf("get = %q, %v", got, err)
	}
}

func TestVCSErrorsNameTheStep(t *testing.T) {
	srv, _ := fakeVCS(t)
	enc, _ := security.NewEncryptor(make([]byte, 32))
	v, _ := NewVCS(srv.URL+"/sign", "wrong-token", enc)
	_, err := v.Put(context.Background(), []byte("x"), ".pdf", "u")
	var se *StorageError
	if !errors.As(err, &se) || se.Step != "grant-upload" || se.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "wrong-token") {
		t.Fatal("errors must not contain URLs or tokens")
	}
}
