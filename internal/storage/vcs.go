package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
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

const (
	vcsPrefix         = "files/"
	uploadContentType = "application/octet-stream" // files are encrypted, so the bytes are opaque
)

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func NewVCS(url, token string, enc *security.Encryptor) (*VCS, error) {
	if url == "" || token == "" {
		return nil, errors.New("VCS storage needs VCS_STORAGE_URL and VCS_STORAGE_TOKEN")
	}
	return &VCS{url: url, token: token, enc: enc, client: &http.Client{Timeout: 60 * time.Second}}, nil
}

type vcsGrant struct {
	URL     string
	Method  string
	Headers map[string]string
}

// rawGrant accepts the grant at the top level or wrapped ({"grant": {...}}),
// with header values as a string or a list of strings.
type rawGrant struct {
	URL     string                     `json:"url"`
	Method  string                     `json:"method"`
	Headers map[string]json.RawMessage `json:"headers"`
	Grant   *rawGrant                  `json:"grant"`
	Data    *rawGrant                  `json:"data"`
}

func (g *rawGrant) flatten() *vcsGrant {
	for _, inner := range []*rawGrant{g.Grant, g.Data} {
		if g.URL == "" && inner != nil {
			return inner.flatten()
		}
	}
	out := &vcsGrant{URL: g.URL, Method: strings.ToUpper(g.Method), Headers: map[string]string{}}
	for k, raw := range g.Headers {
		var one string
		var many []string
		switch {
		case json.Unmarshal(raw, &one) == nil:
			out.Headers[k] = one
		case json.Unmarshal(raw, &many) == nil && len(many) > 0:
			out.Headers[k] = strings.Join(many, ", ")
		}
	}
	return out
}

// StorageError carries what went wrong without any signed URL or token.
type StorageError struct {
	Step   string // grant-upload, put, grant-download, get, grant-delete, delete
	Status int    // HTTP status (0 = no response)
	Code   string // error code from the VCS signer, when it sent one
	Err    error
}

func (e *StorageError) Error() string {
	msg := fmt.Sprintf("vcs storage %s: status %d", e.Step, e.Status)
	if e.Code != "" {
		msg += " (" + e.Code + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *StorageError) Unwrap() error { return e.Err }

// signerCode pulls a short error code/message from the signer's JSON error body.
func signerCode(body []byte) string {
	var b struct {
		Code    string `json:"code"`
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	code := b.Code
	switch e := b.Error.(type) {
	case string:
		if code == "" {
			code = e
		}
	case map[string]any:
		if c, ok := e["code"].(string); ok && code == "" {
			code = c
		}
	}
	if code == "" {
		code = b.Message
	}
	if len(code) > 80 {
		code = code[:80]
	}
	return code
}

// grant asks the signer for one operation. Signed URLs are never logged or stored.
func (v *VCS) grant(ctx context.Context, req map[string]any) (*vcsGrant, error) {
	step := "grant-" + fmt.Sprint(req["action"])
	body, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url, bytes.NewReader(body))
	if err != nil {
		return nil, &StorageError{Step: step, Err: err}
	}
	r.Header.Set("Authorization", "Bearer "+v.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	res, err := v.client.Do(r)
	if err != nil {
		return nil, &StorageError{Step: step, Err: errors.New("signer unreachable: " + netCause(err))}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode/100 != 2 {
		return nil, &StorageError{Step: step, Status: res.StatusCode, Code: signerCode(raw)}
	}
	var rg rawGrant
	if err := json.Unmarshal(raw, &rg); err != nil {
		return nil, &StorageError{Step: step, Status: res.StatusCode, Err: errors.New("grant is not JSON")}
	}
	g := rg.flatten()
	if g.URL == "" {
		return nil, &StorageError{Step: step, Status: res.StatusCode, Err: errors.New("grant has no url")}
	}
	if g.Method == "" {
		g.Method = map[string]string{"upload": http.MethodPut, "download": http.MethodGet, "delete": http.MethodDelete}[fmt.Sprint(req["action"])]
	}
	return g, nil
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
		if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") {
			continue // set by the HTTP client itself
		}
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
		"content_type": uploadContentType})
	if err != nil {
		return "", err
	}
	// The signed URL is bound to this content type; send it even when the
	// grant does not list it, or the bucket rejects the signature.
	if !hasHeader(g.Headers, "content-type") {
		g.Headers["Content-Type"] = uploadContentType
	}
	res, err := v.do(ctx, g, sealed)
	if err != nil {
		return "", &StorageError{Step: "put", Err: errors.New("bucket unreachable: " + netCause(err))}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return "", &StorageError{Step: "put", Status: res.StatusCode}
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
		return nil, &StorageError{Step: "get", Err: errors.New("bucket unreachable: " + netCause(err))}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if res.StatusCode/100 != 2 {
		return nil, &StorageError{Step: "get", Status: res.StatusCode}
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
		return &StorageError{Step: "delete", Err: errors.New("bucket unreachable: " + netCause(err))}
	}
	res.Body.Close()
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound {
		return &StorageError{Step: "delete", Status: res.StatusCode}
	}
	return nil
}

// netCause names the kind of network failure without the URL (which may be signed).
func netCause(err error) string {
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certErr *tls.CertificateVerificationError
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(err, &unknownCA), errors.As(err, &certErr):
		return "TLS certificate not trusted (missing CA bundle?)"
	case errors.As(err, &hostErr):
		return "TLS hostname mismatch"
	case errors.As(err, &dnsErr):
		return "DNS lookup failed"
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "timeout"
	case errors.As(err, &opErr):
		return "connection failed (" + opErr.Op + ")"
	}
	return "network error"
}
