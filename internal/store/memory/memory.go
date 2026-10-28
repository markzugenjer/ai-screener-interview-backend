// Package memory is an in-process implementation of store.Store for tests and
// quick local runs (STORE=memory). Data is lost when the process stops.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

type queueItem struct {
	job      domain.QueueJob
	status   string
	runAfter time.Time
	lastErr  string
}

type Store struct {
	mu         sync.Mutex
	users      map[string]*domain.User
	sessions   map[string]*domain.Session // by token hash
	jobs       map[string]*domain.Job
	questions  map[string][]domain.Question // by job id
	interviews map[string]*domain.Interview
	turns      map[string][]domain.Turn
	scores     map[string]*domain.Score
	reviews    map[string][]domain.Review
	queue      []*queueItem
	audit      []domain.AuditEntry
}

func New() *Store {
	return &Store{
		users:      map[string]*domain.User{},
		sessions:   map[string]*domain.Session{},
		jobs:       map[string]*domain.Job{},
		questions:  map[string][]domain.Question{},
		interviews: map[string]*domain.Interview{},
		turns:      map[string][]domain.Turn{},
		scores:     map[string]*domain.Score{},
		reviews:    map[string][]domain.Review{},
	}
}

func (s *Store) Close()                     {}
func (s *Store) Ping(context.Context) error { return nil }

func clone[T any](v *T) *T { c := *v; return &c }

func (s *Store) CreateUser(_ context.Context, u *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.users {
		if strings.EqualFold(x.Email, u.Email) {
			return store.ErrDuplicate
		}
	}
	s.users[u.ID] = clone(u)
	return nil
}

func (s *Store) GetUserByEmail(_ context.Context, email string) (*domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if strings.EqualFold(u.Email, email) {
			return clone(u), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) GetUserByID(_ context.Context, id string) (*domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[id]; ok {
		return clone(u), nil
	}
	return nil, store.ErrNotFound
}

func (s *Store) UpdateLoginState(_ context.Context, id string, failed int, locked, last *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.FailedLogins, u.LockedUntil = failed, locked
	if last != nil {
		u.LastLoginAt = last
	}
	return nil
}

func (s *Store) SetUserRole(_ context.Context, id string, role domain.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.Role = role
	return nil
}

func (s *Store) CreateSession(_ context.Context, sess *domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.TokenHash] = clone(sess)
	return nil
}

func (s *Store) GetSessionUser(_ context.Context, hash string) (*domain.Session, *domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[hash]
	if !ok || time.Now().After(sess.ExpiresAt) {
		return nil, nil, store.ErrNotFound
	}
	u, ok := s.users[sess.UserID]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	return clone(sess), clone(u), nil
}

func (s *Store) DeleteSession(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hash)
	return nil
}

func (s *Store) DeleteExpiredSessions(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, v := range s.sessions {
		if time.Now().After(v.ExpiresAt) {
			delete(s.sessions, k)
			n++
		}
	}
	return n, nil
}

func (s *Store) jobCopy(j *domain.Job) domain.Job {
	c := *j
	c.QuestionCount = len(s.questions[j.ID])
	return c
}

func (s *Store) ListJobs(_ context.Context, f store.JobFilter) ([]domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(f.Query)
	var out []domain.Job
	for _, j := range s.jobs {
		if f.Status != "" && string(j.Status) != f.Status {
			continue
		}
		if f.Division != "" && j.Division != f.Division {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(j.Title+" "+j.Description), q) {
			continue
		}
		out = append(out, s.jobCopy(j))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}

func (s *Store) GetJob(_ context.Context, id string) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		c := s.jobCopy(j)
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (s *Store) GetJobBySlug(_ context.Context, slug string) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.Slug == slug {
			c := s.jobCopy(j)
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) CreateJob(_ context.Context, j *domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.jobs {
		if x.Slug == j.Slug {
			return store.ErrDuplicate
		}
	}
	c := clone(j)
	c.UpdatedAt = c.CreatedAt
	s.jobs[j.ID] = c
	return nil
}

func (s *Store) UpdateJob(_ context.Context, j *domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.jobs[j.ID]
	if !ok {
		return store.ErrNotFound
	}
	for _, x := range s.jobs {
		if x.ID != j.ID && x.Slug == j.Slug {
			return store.ErrDuplicate
		}
	}
	c := clone(j)
	c.CreatedAt, c.CreatedBy, c.UpdatedAt = old.CreatedAt, old.CreatedBy, time.Now()
	s.jobs[j.ID] = c
	return nil
}

func (s *Store) DeleteJob(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.jobs, id)
	delete(s.questions, id)
	for k, iv := range s.interviews {
		if iv.JobID == id {
			delete(s.interviews, k)
		}
	}
	return nil
}

func (s *Store) ListQuestions(_ context.Context, jobID string) ([]domain.Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Question(nil), s.questions[jobID]...), nil
}

func (s *Store) ReplaceQuestions(_ context.Context, jobID string, qs []domain.Question) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]domain.Question(nil), qs...)
	for i := range cp {
		cp[i].JobID = jobID
	}
	sort.SliceStable(cp, func(a, b int) bool { return cp[a].Sort < cp[b].Sort })
	s.questions[jobID] = cp
	return nil
}

func (s *Store) CreateInterview(_ context.Context, iv *domain.Interview) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := clone(iv)
	c.UpdatedAt = c.CreatedAt
	s.interviews[iv.ID] = c
	return nil
}

func (s *Store) GetInterview(_ context.Context, id string) (*domain.Interview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if iv, ok := s.interviews[id]; ok {
		return clone(iv), nil
	}
	return nil, store.ErrNotFound
}

func (s *Store) UpdateInterview(_ context.Context, iv *domain.Interview) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.interviews[iv.ID]; !ok {
		return store.ErrNotFound
	}
	c := clone(iv)
	c.UpdatedAt = time.Now()
	s.interviews[iv.ID] = c
	return nil
}

func (s *Store) CountCompletedAttempts(_ context.Context, userID, jobID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, iv := range s.interviews {
		if iv.UserID == userID && iv.JobID == jobID &&
			(iv.Status == domain.InterviewCompleted || iv.Status == domain.InterviewScored || iv.Status == domain.InterviewFailed) {
			n++
		}
	}
	return n, nil
}

func (s *Store) CountAttempts(_ context.Context, userID, jobID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, iv := range s.interviews {
		if iv.UserID == userID && iv.JobID == jobID {
			n++
		}
	}
	return n, nil
}

func (s *Store) summary(iv *domain.Interview, staff bool) domain.InterviewSummary {
	out := domain.InterviewSummary{Interview: *iv}
	if j, ok := s.jobs[iv.JobID]; ok {
		out.JobTitle, out.JobSlug, out.JobCategory = j.Title, j.Slug, j.Category
		out.JobSkills, out.JobDivision, out.JobLevel = j.Skills, j.Division, j.Level
	}
	if u, ok := s.users[iv.UserID]; ok {
		out.CandidateName, out.CandidateEmail = u.FullName, u.Email
	}
	if sc, ok := s.scores[iv.ID]; ok {
		o := sc.Overall
		out.Overall = &o
		out.Recommendation = sc.Recommendation
		if staff {
			out.Summary = sc.RecruiterSummary
		} else {
			out.Summary = sc.CandidateSummary
			out.SummaryByLang = map[string]string{}
			for _, l := range domain.FeedbackLanguages {
				out.SummaryByLang[l] = sc.CandidateFor(l).Summary
			}
		}
		for _, d := range sc.Dimensions {
			r := d.Rating
			switch d.Key {
			case "technical":
				out.Technical = &r
			case "communication":
				out.Communication = &r
			}
		}
	}
	if rs := s.reviews[iv.ID]; len(rs) > 0 {
		out.Decision = rs[0].Decision
	}
	return out
}

func (s *Store) ListUserInterviews(_ context.Context, userID string) ([]domain.InterviewSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.InterviewSummary
	for _, iv := range s.interviews {
		if iv.UserID == userID {
			out = append(out, s.summary(iv, false))
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}

func (s *Store) AppendTurns(_ context.Context, id string, turns []domain.Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := len(s.turns[id]) + 1
	for i, t := range turns {
		t.Seq = next + i
		s.turns[id] = append(s.turns[id], t)
	}
	return nil
}

func (s *Store) ListTurns(_ context.Context, id string) ([]domain.Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Turn(nil), s.turns[id]...), nil
}

func (s *Store) ClearTurns(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.turns, id)
	return nil
}

func (s *Store) SaveScore(_ context.Context, sc *domain.Score) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scores[sc.InterviewID] = clone(sc)
	return nil
}

func (s *Store) GetScore(_ context.Context, id string) (*domain.Score, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc, ok := s.scores[id]; ok {
		return clone(sc), nil
	}
	return nil, store.ErrNotFound
}

func (s *Store) AddReview(_ context.Context, r *domain.Review) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *r
	if u, ok := s.users[r.ReviewerID]; ok {
		c.Reviewer = u.FullName
	}
	s.reviews[r.InterviewID] = append([]domain.Review{c}, s.reviews[r.InterviewID]...)
	return nil
}

func (s *Store) ListReviews(_ context.Context, id string) ([]domain.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Review(nil), s.reviews[id]...), nil
}

func (s *Store) ListResults(_ context.Context, f domain.ResultFilter) ([]domain.InterviewSummary, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var all []domain.InterviewSummary
	for _, iv := range s.interviews {
		if iv.Status == domain.InterviewPending || (!f.IncludePractice && iv.IsPractice) {
			continue
		}
		if f.JobID != "" && iv.JobID != f.JobID || f.Status != "" && string(iv.Status) != f.Status {
			continue
		}
		sm := s.summary(iv, true)
		if f.Recommendation != "" && sm.Recommendation != f.Recommendation {
			continue
		}
		if f.MinOverall > 0 && (sm.Overall == nil || *sm.Overall < f.MinOverall) {
			continue
		}
		if f.MinTechnical > 0 && (sm.Technical == nil || *sm.Technical < f.MinTechnical) {
			continue
		}
		if f.Decision == "none" && sm.Decision != "" || f.Decision != "" && f.Decision != "none" && sm.Decision != f.Decision {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(sm.CandidateName+" "+sm.CandidateEmail), q) && !s.transcriptHas(iv.ID, q) {
			continue
		}
		all = append(all, sm)
	}
	overall := func(x domain.InterviewSummary) int {
		if x.Overall == nil {
			return math.MinInt
		}
		return *x.Overall
	}
	sort.Slice(all, func(a, b int) bool {
		switch f.Sort {
		case "-overall":
			return overall(all[a]) > overall(all[b])
		case "overall":
			return overall(all[a]) < overall(all[b])
		case "date":
			return all[a].CreatedAt.Before(all[b].CreatedAt)
		}
		return all[a].CreatedAt.After(all[b].CreatedAt)
	})
	total := len(all)
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if f.Offset >= total {
		return nil, total, nil
	}
	end := min(f.Offset+limit, total)
	return all[f.Offset:end], total, nil
}

func (s *Store) transcriptHas(id, q string) bool {
	for _, t := range s.turns[id] {
		if strings.Contains(strings.ToLower(t.Text), q) {
			return true
		}
	}
	return false
}

func (s *Store) Stats(context.Context) (*domain.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st domain.Stats
	for _, j := range s.jobs {
		if j.Status == domain.JobOpen {
			st.OpenJobs++
		}
	}
	for _, u := range s.users {
		if u.Role == domain.RoleCandidate {
			st.Candidates++
		}
	}
	sum, n := 0, 0
	for _, iv := range s.interviews {
		if iv.IsPractice || iv.Status == domain.InterviewPending {
			continue
		}
		st.InterviewsTotal++
		if iv.Status == domain.InterviewScored {
			st.InterviewsScored++
			if len(s.reviews[iv.ID]) == 0 {
				st.AwaitingReview++
			}
		}
		if sc, ok := s.scores[iv.ID]; ok {
			sum += sc.Overall
			n++
		}
	}
	if n > 0 {
		st.AverageScore = int(math.Round(float64(sum) / float64(n)))
	}
	return &st, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) Enqueue(_ context.Context, kind, payload string, runAfter time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, &queueItem{job: domain.QueueJob{ID: newID(), Kind: kind, Payload: payload}, status: "pending", runAfter: runAfter})
	return nil
}

func (s *Store) ClaimJob(context.Context) (*domain.QueueJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, it := range s.queue {
		if it.status == "pending" && !it.runAfter.After(now) {
			it.status = "running"
			it.job.Attempts++
			j := it.job
			return &j, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) find(id string) *queueItem {
	for _, it := range s.queue {
		if it.job.ID == id {
			return it
		}
	}
	return nil
}

func (s *Store) CompleteJob(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.find(id); it != nil {
		it.status = "done"
	}
	return nil
}

func (s *Store) FailJob(_ context.Context, id, msg string, retryAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.find(id); it != nil {
		it.lastErr = msg
		if retryAt != nil {
			it.status, it.runAfter = "pending", *retryAt
		} else {
			it.status = "failed"
		}
	}
	return nil
}

func (s *Store) Audit(_ context.Context, e domain.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
	if len(s.audit) > 5000 {
		s.audit = s.audit[1000:]
	}
	return nil
}

var _ store.Store = (*Store)(nil)
