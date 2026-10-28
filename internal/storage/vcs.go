package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
)

// VCS stores files in the platform's private bucket. The app never holds bucket
// credentials: for every operation it asks the VCS signer (VCS_STORAGE_URL, with
// the project token) for a short-lived signed URL, then performs that one request.
// Files are encrypted before upload exactly like Local, so the bucket only ever
// sees ciphertext. https://…/skills/vcs/storage.md
type VCS struct {
	url    string
	token  string
	enc    *security.Encryptor
	client *http.Client
}

const vcsPrefix = "files/"

func NewVCS(url, token string, enc *security.Encryptor) (*VCS, error) {
	if url == "" || token == "" {
		return nil, errors.New("VCS storage needs VCS_STORAGE_URL and VCS_STORAGE_TOKEN")
	}
	return &VCS{url: url, token: token, enc: enc, client: &http.Client{Timeout: 60 * time.Second}}, nil
}

type vcsGrant struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
}

// grant asks the signer for one operation. Signed URLs are never logged or stored.
func (v *VCS) grant(ctx context.Context, req map[string]any) (*vcsGrant, error) {
	body, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+v.token)
	r.Header.Set("Content-Type", "application/json")
	res, err := v.client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("vcs storage: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("vcs storage %s: status %d", req["action"], res.StatusCode)
	}
	var g vcsGrant
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&g); err != nil || g.URL == "" {
		return nil, fmt.Errorf("vcs storage %s: bad grant", req["action"])
	}
	if g.Method == "" {
		g.Method = map[string]string{"upload": http.MethodPut, "download": http.MethodGet, "delete": http.MethodDelete}[fmt.Sprint(req["action"])]
	}
	return &g, nil
}

func (v *VCS) do(ctx context.Context, g *vcsGrant, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	r, err := http.NewRequestWithContext(ctx, g.Method, g.URL, rd)
	if err != nil {
		return nil, err
	}
	for k, val := range g.Headers {
		r.Header.Set(k, val)
	}
	if body != nil {
		r.ContentLength = int64(len(body))
	}
	return v.client.Do(r)
}

func (v *VCS) Put(ctx context.Context, data []byte, ext, owner string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b) + ext + ".enc"
	if !keyPattern.MatchString(key) {
		return "", ErrBadKey
	}
	sealed, err := v.enc.Encrypt(data, []byte("file:"+owner))
	if err != nil {
		return "", err
	}
	g, err := v.grant(ctx, map[string]any{"action": "upload", "key": vcsPrefix + key, "size": len(sealed),
		"content_type": "application/octet-stream"})
	if err != nil {
		return "", err
	}
	res, err := v.do(ctx, g, sealed)
	if err != nil {
		return "", fmt.Errorf("vcs storage upload: %w", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("vcs storage upload: status %d", res.StatusCode)
	}
	return key, nil
}

func (v *VCS) Get(ctx context.Context, key, owner string) ([]byte, error) {
	if !keyPattern.MatchString(key) {
		return nil, ErrBadKey
	}
	g, err := v.grant(ctx, map[string]any{"action": "download", "key": vcsPrefix + key})
	if err != nil {
		return nil, err
	}
	res, err := v.do(ctx, g, nil)
	if err != nil {
		return nil, fmt.Errorf("vcs storage download: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("vcs storage download: status %d", res.StatusCode)
	}
	sealed, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	return v.enc.Decrypt(sealed, []byte("file:"+owner))
}

func (v *VCS) Delete(ctx context.Context, key string) error {
	if !keyPattern.MatchString(key) {
		return ErrBadKey
	}
	g, err := v.grant(ctx, map[string]any{"action": "delete", "key": vcsPrefix + key})
	if err != nil {
		return err
	}
	res, err := v.do(ctx, g, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("vcs storage delete: status %d", res.StatusCode)
	}
	return nil
}
