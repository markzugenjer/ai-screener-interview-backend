package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/app"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/config"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/seed"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/storage"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store/memory"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/worker"
)

const internalSecret = "test-internal-secret-0123456789abcdef"

type env struct {
	t     *testing.T
	srv   *httptest.Server
	store *memory.Store
	api   *API
}

func setup(t *testing.T) *env {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cfg := &config.Config{
		Env: "test", PublicURL: "http://app.test", AllowedOrigins: []string{"http://app.test"},
		EncryptionKey: key, SessionTTL: time.Hour, InternalSecret: internalSecret, MaxResumeBytes: 1 << 20,
		LiveKitURL: "wss://example.livekit.cloud", LiveKitAPIKey: "APIkey", LiveKitAPISecret: "secret-secret-secret",
		LiveKitAgentName: "ai-screener", StorageDir: t.TempDir(),
	}
	st := memory.New()
	if _, err := seed.Demo(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	enc, _ := security.NewEncryptor(key)
	files, _ := storage.NewLocal(cfg.StorageDir, enc)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := New(cfg, st, enc, files, log)
	a.authLimit = security.NewRateLimiter(1000, 1000) // rate limiting has its own unit test
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, store: st, api: a}
}

type client struct {
	e    *env
	http *http.Client
	csrf string
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	c := &client{e: e, http: &http.Client{Jar: jar}}
	var out struct{ Token string }
	c.do("GET", "/api/v1/auth/csrf", nil, "", &out)
	c.csrf = out.Token
	return c
}

func (c *client) do(method, path string, body io.Reader, contentType string, out any) int {
	c.e.t.Helper()
	req, _ := http.NewRequest(method, c.e.srv.URL+path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.csrf != "" {
		req.Header.Set(csrfHeader, c.csrf)
	}
	req.Header.Set("Origin", "http://app.test")
	res, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.e.t.Fatalf("%s %s: bad json %q", method, path, raw)
		}
	}
	return res.StatusCode
}

func (c *client) json(method, path string, in, out any) int {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	return c.do(method, path, body, "application/json", out)
}

func registerForm(fields map[string]string, resume []byte) (io.Reader, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if resume != nil {
		fw, _ := mw.CreateFormFile("resume", "CV Budi.pdf")
		_, _ = fw.Write(resume)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

var goodFields = map[string]string{
	"fullName": "Budi Santoso", "email": "Budi@Mail.com", "password": "rahasia123",
	"confirmPassword": "rahasia123", "whatsapp": "0812-3456-7890",
}

type fakeLLM struct{}

func (fakeLLM) Model() string { return "fake" }
func (fakeLLM) Complete(context.Context, string, string, int) (string, error) {
	return `{"questions":[],"ratings":{"technical":4,"problemSolving":4,"communication":4,"motivation":5},
	"notes":{},"recruiterSummary":"Strong junior.",
	"candidate":{"en":{"summary":"Great job!","strengths":["Clear"],"improvements":["More examples"],"tips":["Keep practicing"]},
	"id":{"summary":"Kerja bagus!","strengths":["Jelas"],"improvements":["Lebih banyak contoh"],"tips":["Terus berlatih"]}}}`, nil
}

func TestFullCandidateAndRecruiterFlow(t *testing.T) {
	e := setup(t)
	c := e.client()

	// Validation errors are reported per field.
	body, ct := registerForm(map[string]string{"fullName": "B", "email": "nope", "password": "short",
		"confirmPassword": "x", "whatsapp": "123"}, []byte("<html>"))
	var verr struct {
		Error apiError `json:"error"`
	}
	if code := c.do("POST", "/api/v1/auth/register", body, ct, &verr); code != 422 {
		t.Fatalf("register invalid: %d", code)
	}
	for _, f := range []string{"fullName", "email", "password", "confirmPassword", "whatsapp", "resume"} {
		if verr.Error.Fields[f] == "" {
			t.Errorf("missing field error for %s", f)
		}
	}

	// Register succeeds and logs the user in.
	body, ct = registerForm(goodFields, []byte("%PDF-1.4 my resume"))
	var reg struct{ User userDTO }
	if code := c.do("POST", "/api/v1/auth/register", body, ct, &reg); code != 201 {
		t.Fatalf("register: %d", code)
	}
	if reg.User.Email != "budi@mail.com" || reg.User.Role != "candidate" || reg.User.WhatsApp != "+62812****7890" {
		t.Fatalf("unexpected user %+v", reg.User)
	}
	u, _ := e.store.GetUserByEmail(context.Background(), "budi@mail.com")
	if strings.Contains(u.PhoneEnc, "812") || u.PasswordHash == "rahasia123" {
		t.Fatal("phone must be encrypted and password hashed")
	}

	// Duplicate email.
	body, ct = registerForm(goodFields, []byte("%PDF-1.4"))
	if code := e.client().do("POST", "/api/v1/auth/register", body, ct, nil); code != 422 {
		t.Fatalf("duplicate register: %d", code)
	}

	// Job list and start interview.
	var jobs struct{ Jobs []jobDTO }
	c.json("GET", "/api/v1/jobs", nil, &jobs)
	if len(jobs.Jobs) != 2 || jobs.Jobs[0].Weights != nil {
		t.Fatalf("jobs: %+v", jobs.Jobs)
	}
	var started struct{ Interview interviewDTO }
	if code := c.json("POST", "/api/v1/jobs/junior-frontend-developer/interviews", nil, &started); code != 201 {
		t.Fatalf("start: %d", code)
	}
	ivID := started.Interview.ID

	// Another user cannot see it.
	other := e.client()
	body, ct = registerForm(map[string]string{"fullName": "Sari Dewi", "email": "sari@mail.com",
		"password": "rahasia123", "confirmPassword": "rahasia123", "whatsapp": "081298765432"}, []byte("%PDF-1.4"))
	other.do("POST", "/api/v1/auth/register", body, ct, nil)
	if code := other.json("GET", "/api/v1/interviews/"+ivID, nil, nil); code != 404 {
		t.Fatalf("other user should get 404, got %d", code)
	}

	// LiveKit token is a signed JWT with the agent dispatch.
	var tok struct{ Token, URL string }
	if code := c.json("POST", "/api/v1/interviews/"+ivID+"/token", nil, &tok); code != 200 || strings.Count(tok.Token, ".") != 2 {
		t.Fatalf("token: %d %q", code, tok.Token)
	}

	// Reset is allowed once, early.
	if code := c.json("POST", "/api/v1/interviews/"+ivID+"/reset", nil, nil); code != 200 {
		t.Fatalf("reset: %d", code)
	}
	if code := c.json("POST", "/api/v1/interviews/"+ivID+"/reset", nil, nil); code != 409 {
		t.Fatalf("second reset should be refused: %d", code)
	}

	// The voice agent fetches context and streams transcript turns.
	agent := func(method, path string, in any) int {
		b, _ := json.Marshal(in)
		req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+internalSecret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := agent("GET", "/internal/interviews/"+ivID+"/context", nil); code != 200 {
		t.Fatalf("context: %d", code)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/internal/interviews/"+ivID+"/context", nil)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 401 {
		t.Fatalf("internal endpoint without secret: %d", res.StatusCode)
	}
	answer := strings.Repeat("saya menggunakan const untuk nilai yang tidak berubah ", 6)
	turns := map[string]any{"turns": []map[string]any{
		{"speaker": "ai", "text": "Apa perbedaan let dan const?", "startMs": 0, "endMs": 3000},
		{"speaker": "candidate", "text": answer, "startMs": 3500, "endMs": 30000},
	}}
	if code := agent("POST", "/internal/interviews/"+ivID+"/turns", turns); code != 200 {
		t.Fatalf("turns: %d", code)
	}

	// Candidate leaves -> completed -> worker scores it.
	var fin struct{ Interview interviewDTO }
	c.json("POST", "/api/v1/interviews/"+ivID+"/finish", nil, &fin)
	if fin.Interview.Status != "completed" {
		t.Fatalf("finish status %s", fin.Interview.Status)
	}
	w := &worker.Worker{Store: e.store, LLM: fakeLLM{}, Language: "English", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Poll: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go w.Run(ctx)
	var fb struct {
		Feedback *candidateScoreDTO
	}
	for ctx.Err() == nil {
		c.json("GET", "/api/v1/interviews/"+ivID+"/feedback", nil, &fb)
		if fb.Feedback != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if fb.Feedback == nil || fb.Feedback.Overall == 0 || fb.Feedback.Summary != "Great job!" {
		t.Fatalf("feedback: %+v", fb.Feedback)
	}
	var fbID struct{ Feedback *candidateScoreDTO }
	c.json("GET", "/api/v1/interviews/"+ivID+"/feedback?lang=id", nil, &fbID)
	if fbID.Feedback == nil || fbID.Feedback.Summary != "Kerja bagus!" || fbID.Feedback.Tips[0] != "Terus berlatih" ||
		!strings.HasPrefix(fbID.Feedback.Dimensions[3].Notes[0], "Berbicara sekitar") {
		t.Fatalf("indonesian feedback: %+v", fbID.Feedback)
	}
	var mine struct{ Interviews []interviewDTO }
	c.json("GET", "/api/v1/me/interviews?lang=id", nil, &mine)
	if len(mine.Interviews) == 0 || mine.Interviews[0].Summary != "Kerja bagus!" {
		t.Fatalf("indonesian list summary: %+v", mine.Interviews)
	}
	var rawFb map[string]any
	c.json("GET", "/api/v1/interviews/"+ivID+"/feedback", nil, &rawFb)
	if s, _ := json.Marshal(rawFb); strings.Contains(string(s), "Strong junior") || strings.Contains(string(s), "recommendation") {
		t.Fatal("candidate must not see recruiter-only data")
	}

	// Candidate cannot reach admin endpoints.
	if code := c.json("GET", "/api/v1/admin/results", nil, nil); code != 403 {
		t.Fatalf("candidate admin access: %d", code)
	}

	// Recruiter sees, filters and reviews the result.
	if _, err := app.CreateStaff(context.Background(), e.store, "Rina Recruiter", "rina@company.com", "recruiter-pass-123", domain.RoleRecruiter); err != nil {
		t.Fatal(err)
	}
	r := e.client()
	if code := r.json("POST", "/api/v1/auth/login", map[string]string{"email": "rina@company.com", "password": "recruiter-pass-123"}, nil); code != 200 {
		t.Fatalf("recruiter login: %d", code)
	}
	var results struct {
		Results []resultRowDTO
		Total   int
	}
	r.json("GET", "/api/v1/admin/results?minOverall=50&sort=-overall", nil, &results)
	if results.Total != 1 || results.Results[0].CandidateName != "Budi Santoso" || results.Results[0].Recommendation == "" {
		t.Fatalf("results: %+v", results)
	}
	r.json("GET", "/api/v1/admin/results?minOverall=99", nil, &results)
	if results.Total != 0 {
		t.Fatal("filter minOverall failed")
	}
	var detail struct {
		Candidate map[string]any
		Score     *domain.Score
	}
	r.json("GET", "/api/v1/admin/results/"+ivID, nil, &detail)
	if detail.Candidate["whatsapp"] != "+6281234567890" || detail.Score.RecruiterSummary != "Strong junior." {
		t.Fatalf("detail: %+v", detail.Candidate)
	}
	if code := r.json("POST", "/api/v1/admin/results/"+ivID+"/review", map[string]string{"decision": "shortlist", "notes": "Invite to HR interview"}, nil); code != 201 {
		t.Fatalf("review: %d", code)
	}
	r.json("GET", "/api/v1/admin/results?decision=shortlist", nil, &results)
	if results.Total != 1 {
		t.Fatal("decision filter failed")
	}

	// Resume download decrypts the file.
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/v1/admin/candidates/"+u.ID+"/resume", nil)
	res, err := r.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(data) != "%PDF-1.4 my resume" {
		t.Fatalf("resume download: %d %q", res.StatusCode, data)
	}

	// A second attempt is marked as practice.
	var second struct{ Interview interviewDTO }
	c.json("POST", "/api/v1/jobs/junior-frontend-developer/interviews", nil, &second)
	if !second.Interview.IsPractice || second.Interview.Attempt != 2 {
		t.Fatalf("second attempt: %+v", second.Interview)
	}
}

func TestCSRFAndOriginProtection(t *testing.T) {
	e := setup(t)
	c := e.client()
	b, _ := json.Marshal(map[string]string{"email": "x@y.co", "password": "whatever1"})

	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, _ := c.http.Do(req) // cookie present, header missing
	if res.StatusCode != 403 {
		t.Fatalf("missing csrf header: %d", res.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.srv.URL+"/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, c.csrf)
	req.Header.Set("Origin", "https://evil.example")
	res, _ = c.http.Do(req)
	if res.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", res.StatusCode)
	}
}

func TestLoginLockout(t *testing.T) {
	e := setup(t)
	c := e.client()
	body, ct := registerForm(goodFields, []byte("%PDF-1.4"))
	c.do("POST", "/api/v1/auth/register", body, ct, nil)
	a := e.client()
	for i := 0; i < maxFailedLogins; i++ {
		if code := a.json("POST", "/api/v1/auth/login", map[string]string{"email": "budi@mail.com", "password": "wrongpass1"}, nil); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := a.json("POST", "/api/v1/auth/login", map[string]string{"email": "budi@mail.com", "password": "rahasia123"}, nil); code != 429 {
		t.Fatalf("account should be locked, got %d", code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := setup(t)
	res, err := http.Get(e.srv.URL + "/api/v1/jobs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if res.Header.Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

func TestSameHostOrigin(t *testing.T) {
	req := func(host, proto string) *http.Request {
		r, _ := http.NewRequest("POST", "http://127.0.0.1:8081/api/v1/auth/login", nil)
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Proto", proto)
		return r
	}
	off := &API{cfg: &config.Config{Env: "production", AllowedOrigins: []string{"https://listed.example"}}}
	if off.originAllowed(req("app.example", "https"), "https://app.example") {
		t.Fatal("same-host origin must be off by default")
	}
	if !off.originAllowed(req("", ""), "https://listed.example") {
		t.Fatal("configured origin must pass")
	}
	on := &API{cfg: &config.Config{Env: "production", SameHostOrigin: true}}
	cases := []struct {
		host, proto, origin string
		want                bool
	}{
		{"app.example", "https", "https://app.example", true},
		{"app.example", "https,http", "https://app.example", true},
		{"app.example", "https", "https://evil.example", false},
		{"app.example", "http", "http://app.example", false}, // no plain http in production
		{"", "https", "https://", false},
		{"app.example", "ftp", "ftp://app.example", false},
	}
	for _, c := range cases {
		if got := on.originAllowed(req(c.host, c.proto), c.origin); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestStorageSelfTestIsAdminOnly(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := app.CreateStaff(ctx, e.store, "Rina", "rina@company.com", "recruiter-pass-123", domain.RoleRecruiter); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CreateStaff(ctx, e.store, "Ada", "ada@company.com", "admin-pass-12345", domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	rec := e.client()
	rec.json("POST", "/api/v1/auth/login", map[string]string{"email": "rina@company.com", "password": "recruiter-pass-123"}, nil)
	if code := rec.json("GET", "/api/v1/admin/diagnostics/storage", nil, nil); code != 403 {
		t.Fatalf("recruiter must not run the self-test: %d", code)
	}
	adm := e.client()
	adm.json("POST", "/api/v1/auth/login", map[string]string{"email": "ada@company.com", "password": "admin-pass-12345"}, nil)
	var out struct {
		Backend string
		OK      bool
		Steps   []diagStep
	}
	if code := adm.json("GET", "/api/v1/admin/diagnostics/storage", nil, &out); code != 200 || !out.OK || len(out.Steps) != 3 {
		t.Fatalf("self-test: %d %+v", code, out)
	}
}

// fakeTranslator answers job translations with a fixed English text.
type fakeTranslator struct{ calls int }

func (f *fakeTranslator) Complete(context.Context, string, string, int) (string, error) {
	f.calls++
	return `{"title":"Clinic Staff","division":"Operations","category":"Non-Technical","location":"Jakarta",
	"employmentType":"Full-time","description":"FDC Dental Clinic is looking for clinic staff.","requirements":["High school diploma"]}`, nil
}
func (f *fakeTranslator) Model() string { return "fake" }

func TestJobsFollowTheVisitorsLanguage(t *testing.T) {
	e := setup(t)
	tr := &fakeTranslator{}
	e.api.WithTranslator(tr)
	ctx := context.Background()
	if _, err := app.CreateStaff(ctx, e.store, "Ada", "ada@company.com", "admin-pass-12345", domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	adm := e.client()
	adm.json("POST", "/api/v1/auth/login", map[string]string{"email": "ada@company.com", "password": "admin-pass-12345"}, nil)
	q := func(n int) map[string]any {
		return map[string]any{"skill": "Pelayanan", "difficulty": 1, "type": "soft", "text": fmt.Sprintf("Pertanyaan nomor %d?", n), "expectedPoints": []string{"x"}}
	}
	job := map[string]any{"title": "Staff Klinik", "division": "Operasional", "category": "Non-Teknis", "level": "junior",
		"location": "Jakarta", "employmentType": "Penuh waktu", "description": "FDC Dental Clinic sedang mencari staff klinik.",
		"requirements": []string{"Minimal SMA"}, "status": "open", "sourceLang": "id",
		"questions": []map[string]any{q(1), q(2), q(3)}}
	var created struct {
		Job        jobDTO
		Translated []string
	}
	if code := adm.json("POST", "/api/v1/admin/jobs", job, &created); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if tr.calls != 1 || len(created.Translated) != 1 || created.Translated[0] != "en" {
		t.Fatalf("auto-translate: calls=%d translated=%v", tr.calls, created.Translated)
	}
	if created.Job.Title != "Staff Klinik" || created.Job.Translations["en"].Title != "Clinic Staff" {
		t.Fatalf("editor view: %+v", created.Job)
	}

	visitor := e.client()
	var en, id struct{ Job jobDTO }
	visitor.json("GET", "/api/v1/jobs/"+created.Job.Slug+"?lang=en", nil, &en)
	visitor.json("GET", "/api/v1/jobs/"+created.Job.Slug+"?lang=id", nil, &id)
	if en.Job.Title != "Clinic Staff" || en.Job.Requirements[0] != "High school diploma" || en.Job.Lang != "en" {
		t.Fatalf("english: %+v", en.Job)
	}
	if id.Job.Title != "Staff Klinik" || id.Job.Requirements[0] != "Minimal SMA" || id.Job.Lang != "id" {
		t.Fatalf("indonesian: %+v", id.Job)
	}
	if en.Job.Translations != nil {
		t.Fatal("visitors must not receive the editor's translation map")
	}

	// A recruiter's own translation is kept as written (no model call).
	job["translations"] = map[string]any{"en": map[string]any{"title": "Clinic Assistant", "description": "Own text"}}
	var updated struct{ Job jobDTO }
	if code := adm.json("PUT", "/api/v1/admin/jobs/"+created.Job.ID, job, &updated); code != 200 {
		t.Fatalf("update: %d", code)
	}
	if tr.calls != 1 || updated.Job.Translations["en"].Title != "Clinic Assistant" {
		t.Fatalf("manual translation: calls=%d %+v", tr.calls, updated.Job.Translations)
	}
	visitor.json("GET", "/api/v1/jobs?lang=en", nil, &struct{}{})
	var list struct{ Jobs []jobDTO }
	visitor.json("GET", "/api/v1/jobs?lang=en", nil, &list)
	found := false
	for _, j := range list.Jobs {
		if j.Slug == created.Job.Slug {
			found = j.Title == "Clinic Assistant" && j.Division == "Operasional" // missing field falls back
		}
	}
	if !found {
		t.Fatalf("job board in english: %+v", list.Jobs)
	}
}

// fakeJobi is a minimal JOBI provider: it checks the client credentials, the
// redirect URI and the PKCE verifier, then returns the employee's profile.
func fakeJobi(t *testing.T, sub, email string) *httptest.Server {
	var challenge, redirect string
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		challenge, redirect = r.URL.Query().Get("code_challenge"), r.URL.Query().Get("redirect_uri")
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || id != "client-1" || secret != "secret-1" || r.PostForm.Get("code") != "good-code" ||
			r.PostForm.Get("redirect_uri") != redirect || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1", "token_type": "Bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": sub, "name": "Siti Rahma", "email": email, "email_verified": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestJobiSignIn(t *testing.T) {
	e := setup(t)
	jobi := fakeJobi(t, "jobi-123", "siti@fdcdentalclinic.co.id")
	e.api.cfg.Jobi = config.JobiConfig{ClientID: "client-1", ClientSecret: "secret-1", AuthorizeURL: jobi.URL + "/authorize",
		TokenURL: jobi.URL + "/token", UserinfoURL: jobi.URL + "/userinfo", CallbackPath: "/api/v1/auth/jobi/callback", Scopes: "profile email"}

	var prov map[string]bool
	e.client().json("GET", "/api/v1/auth/providers", nil, &prov)
	if !prov["jobi"] {
		t.Fatalf("providers: %v", prov)
	}

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	signIn := func(code string) *http.Response {
		res, err := browser.Get(e.srv.URL + "/api/v1/auth/jobi/login?next=/admin/jobs")
		if err != nil || res.StatusCode != http.StatusSeeOther {
			t.Fatalf("login: %v %v", err, res)
		}
		loc, _ := url.Parse(res.Header.Get("Location"))
		if !strings.HasPrefix(loc.String(), jobi.URL+"/authorize") || loc.Query().Get("code_challenge_method") != "S256" {
			t.Fatalf("authorize redirect: %s", loc)
		}
		if _, err := http.Get(loc.String()); err != nil { // the provider records challenge + redirect_uri
			t.Fatal(err)
		}
		cb := e.srv.URL + "/api/v1/auth/jobi/callback?code=" + code + "&state=" + url.QueryEscape(loc.Query().Get("state"))
		res, err = browser.Get(cb)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := signIn("good-code")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/jobs" {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	u, err := e.store.GetUserByJobiSub(context.Background(), "jobi-123")
	if err != nil || u.Role != domain.RoleRecruiter || u.PasswordHash != "" || u.FullName != "Siti Rahma" {
		t.Fatalf("jobi user: %+v %v", u, err)
	}
	// The session works and the user may use the recruiter area.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/auth/me", nil)
	meRes, _ := browser.Do(req)
	var me struct{ User userDTO }
	_ = json.NewDecoder(meRes.Body).Decode(&me)
	if !me.User.CanRecruit || !me.User.ViaJobi {
		t.Fatalf("me: %+v", me.User)
	}
	// Signing in again reuses the same account.
	signIn("good-code")
	if n := len(e.store.ListUsersForTest()); n != 1 {
		t.Fatalf("users after second sign-in: %d", n)
	}
	// A JOBI account has no password: password login must fail.
	if code := e.client().json("POST", "/api/v1/auth/login", map[string]string{"email": "siti@fdcdentalclinic.co.id", "password": ""}, nil); code == 200 {
		t.Fatal("JOBI account must not log in with an empty password")
	}

	// A wrong state is refused, and a bad code fails safely.
	bad, _ := browser.Get(e.srv.URL + "/api/v1/auth/jobi/callback?code=good-code&state=forged")
	if bad.Header.Get("Location") != "/login?error=jobi_expired" {
		t.Fatalf("forged state: %s", bad.Header.Get("Location"))
	}
	if res := signIn("bad-code"); res.Header.Get("Location") != "/login?error=jobi" {
		t.Fatalf("bad code: %s", res.Header.Get("Location"))
	}
	// An open redirect via ?next is not possible.
	if safeNext("//evil.example") != "" || safeNext("https://evil.example") != "" || safeNext("/admin") != "/admin" {
		t.Fatal("safeNext")
	}
}

func TestOpenRecruiterAccess(t *testing.T) {
	e := setup(t)
	c := e.client()
	body, ct := registerForm(goodFields, []byte("%PDF-1.4"))
	c.do("POST", "/api/v1/auth/register", body, ct, nil)
	if code := c.json("GET", "/api/v1/admin/jobs", nil, nil); code != 403 {
		t.Fatalf("closed by default: %d", code)
	}
	e.api.cfg.OpenRecruiterAccess = true
	if code := c.json("GET", "/api/v1/admin/jobs", nil, nil); code != 200 {
		t.Fatalf("open access: %d", code)
	}
	if code := c.json("GET", "/api/v1/admin/diagnostics/storage", nil, nil); code != 403 {
		t.Fatalf("admin-only routes stay admin-only: %d", code)
	}
}
