// Package postgres implements store.Store on PostgreSQL using pgx.
// All queries use bind parameters ($1, $2…), never string-built values, to prevent SQL injection.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close()                         { d.pool.Close() }
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// Migrate applies embedded SQL files in name order, once each, inside a transaction.
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		name := strings.TrimPrefix(f, "migrations/")
		sqlBytes, err := migrationFS.ReadFile(f)
		if err != nil {
			return err
		}
		tx, err := d.pool.Begin(ctx)
		if err != nil {
			return err
		}
		// A transaction-scoped advisory lock serializes concurrent API/worker startups
		// and is released automatically on commit or rollback.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(726354)`); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&exists); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if exists {
			_ = tx.Rollback(ctx)
			continue
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		slog.Info("applied migration", "name", name)
	}
	return nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return store.ErrDuplicate
	}
	return err
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func fromJSON[T any](b []byte, dst *T) {
	if len(b) > 0 {
		_ = json.Unmarshal(b, dst)
	}
}

// ---------- Users ----------

const userCols = `id::text, role, full_name, email, password_hash, phone_enc, resume_key, resume_name,
	resume_mime, failed_logins, locked_until, last_login_at, created_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var role string
	err := row.Scan(&u.ID, &role, &u.FullName, &u.Email, &u.PasswordHash, &u.PhoneEnc, &u.ResumeKey,
		&u.ResumeName, &u.ResumeMime, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt, &u.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	u.Role = domain.Role(role)
	return &u, nil
}

func (d *DB) CreateUser(ctx context.Context, u *domain.User) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO users (id, role, full_name, email, password_hash, phone_enc,
		resume_key, resume_name, resume_mime, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		u.ID, string(u.Role), u.FullName, u.Email, u.PasswordHash, u.PhoneEnc, u.ResumeKey, u.ResumeName,
		u.ResumeMime, u.CreatedAt)
	return mapErr(err)
}

func (d *DB) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return scanUser(d.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(email) = lower($1)`, email))
}

func (d *DB) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	return scanUser(d.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (d *DB) UpdateLoginState(ctx context.Context, userID string, failed int, lockedUntil, lastLogin *time.Time) error {
	_, err := d.pool.Exec(ctx, `UPDATE users SET failed_logins = $2, locked_until = $3,
		last_login_at = COALESCE($4, last_login_at), updated_at = now() WHERE id = $1`,
		userID, failed, lockedUntil, lastLogin)
	return err
}

func (d *DB) SetUserRole(ctx context.Context, userID string, role domain.Role) error {
	tag, err := d.pool.Exec(ctx, `UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, userID, string(role))
	if err == nil && tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return err
}

func (d *DB) CreateSession(ctx context.Context, s *domain.Session) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, user_agent, ip, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, s.ID, s.UserID, s.TokenHash, s.UserAgent, s.IP, s.ExpiresAt, s.CreatedAt)
	return mapErr(err)
}

func (d *DB) GetSessionUser(ctx context.Context, tokenHash string) (*domain.Session, *domain.User, error) {
	var s domain.Session
	var u domain.User
	var role string
	err := d.pool.QueryRow(ctx, `SELECT s.id::text, s.user_id::text, s.token_hash, s.expires_at, s.created_at,
		u.id::text, u.role, u.full_name, u.email, u.password_hash, u.phone_enc, u.resume_key, u.resume_name,
		u.resume_mime, u.failed_logins, u.locked_until, u.last_login_at, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash).Scan(
		&s.ID, &s.UserID, &s.TokenHash, &s.ExpiresAt, &s.CreatedAt,
		&u.ID, &role, &u.FullName, &u.Email, &u.PasswordHash, &u.PhoneEnc, &u.ResumeKey, &u.ResumeName,
		&u.ResumeMime, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt, &u.CreatedAt)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	u.Role = domain.Role(role)
	return &s, &u, nil
}

func (d *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (d *DB) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}

// ---------- Jobs ----------

const jobCols = `j.id::text, j.title, j.slug, j.division, j.category, j.level, j.location, j.employment_type,
	j.description, j.requirements, j.skills, j.duration_minutes, j.status, j.weights,
	COALESCE(j.created_by::text, ''), j.created_at, j.updated_at,
	(SELECT count(*) FROM questions q WHERE q.job_id = j.id)`

func scanJob(row pgx.Row) (*domain.Job, error) {
	var j domain.Job
	var status string
	var reqs, skills, weights []byte
	err := row.Scan(&j.ID, &j.Title, &j.Slug, &j.Division, &j.Category, &j.Level, &j.Location, &j.EmploymentType,
		&j.Description, &reqs, &skills, &j.DurationMinutes, &status, &weights, &j.CreatedBy, &j.CreatedAt,
		&j.UpdatedAt, &j.QuestionCount)
	if err != nil {
		return nil, mapErr(err)
	}
	j.Status = domain.JobStatus(status)
	fromJSON(reqs, &j.Requirements)
	fromJSON(skills, &j.Skills)
	fromJSON(weights, &j.Weights)
	return &j, nil
}

func (d *DB) ListJobs(ctx context.Context, f store.JobFilter) ([]domain.Job, error) {
	q := `SELECT ` + jobCols + ` FROM jobs j WHERE ($1 = '' OR j.status = $1)
		AND ($2 = '' OR j.division = $2)
		AND ($3 = '' OR j.title ILIKE '%' || $3 || '%' OR j.description ILIKE '%' || $3 || '%')
		ORDER BY j.created_at DESC LIMIT 200`
	rows, err := d.pool.Query(ctx, q, f.Status, f.Division, escapeLike(f.Query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (d *DB) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	return scanJob(d.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs j WHERE j.id = $1`, id))
}

func (d *DB) GetJobBySlug(ctx context.Context, slug string) (*domain.Job, error) {
	return scanJob(d.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs j WHERE j.slug = $1`, slug))
}

func (d *DB) CreateJob(ctx context.Context, j *domain.Job) error {
	var createdBy any
	if j.CreatedBy != "" {
		createdBy = j.CreatedBy
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO jobs (id, title, slug, division, category, level, location,
		employment_type, description, requirements, skills, duration_minutes, status, weights, created_by,
		created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)`,
		j.ID, j.Title, j.Slug, j.Division, j.Category, j.Level, j.Location, j.EmploymentType, j.Description,
		toJSON(j.Requirements), toJSON(j.Skills), j.DurationMinutes, string(j.Status), toJSON(j.Weights),
		createdBy, j.CreatedAt)
	return mapErr(err)
}

func (d *DB) UpdateJob(ctx context.Context, j *domain.Job) error {
	tag, err := d.pool.Exec(ctx, `UPDATE jobs SET title=$2, slug=$3, division=$4, category=$5, level=$6,
		location=$7, employment_type=$8, description=$9, requirements=$10, skills=$11, duration_minutes=$12,
		status=$13, weights=$14, updated_at=now() WHERE id=$1`,
		j.ID, j.Title, j.Slug, j.Division, j.Category, j.Level, j.Location, j.EmploymentType, j.Description,
		toJSON(j.Requirements), toJSON(j.Skills), j.DurationMinutes, string(j.Status), toJSON(j.Weights))
	if err == nil && tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return mapErr(err)
}

func (d *DB) DeleteJob(ctx context.Context, id string) error {
	tag, err := d.pool.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return err
}

func (d *DB) ListQuestions(ctx context.Context, jobID string) ([]domain.Question, error) {
	rows, err := d.pool.Query(ctx, `SELECT id::text, job_id::text, skill, difficulty, type, text,
		expected_points, follow_ups, sort FROM questions WHERE job_id = $1 ORDER BY sort, difficulty`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Question
	for rows.Next() {
		var q domain.Question
		var ep, fu []byte
		if err := rows.Scan(&q.ID, &q.JobID, &q.Skill, &q.Difficulty, &q.Type, &q.Text, &ep, &fu, &q.Sort); err != nil {
			return nil, err
		}
		fromJSON(ep, &q.ExpectedPoints)
		fromJSON(fu, &q.FollowUps)
		out = append(out, q)
	}
	return out, rows.Err()
}

func (d *DB) ReplaceQuestions(ctx context.Context, jobID string, qs []domain.Question) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `DELETE FROM questions WHERE job_id = $1`, jobID); err != nil {
		return err
	}
	for _, q := range qs {
		if _, err := tx.Exec(ctx, `INSERT INTO questions (id, job_id, skill, difficulty, type, text,
			expected_points, follow_ups, sort) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			q.ID, jobID, q.Skill, q.Difficulty, q.Type, q.Text, toJSON(q.ExpectedPoints), toJSON(q.FollowUps), q.Sort); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit(ctx)
}

// ---------- Interviews ----------

const interviewCols = `i.id::text, i.job_id::text, i.user_id::text, i.status, i.attempt, i.is_practice,
	i.room_name, i.reset_count, i.started_at, i.ended_at, i.duration_seconds, i.integrity, i.last_error,
	i.created_at, i.updated_at`

type interviewScan struct {
	iv        domain.Interview
	status    string
	integrity []byte
}

func (s *interviewScan) targets() []any {
	return []any{&s.iv.ID, &s.iv.JobID, &s.iv.UserID, &s.status, &s.iv.Attempt, &s.iv.IsPractice,
		&s.iv.RoomName, &s.iv.ResetCount, &s.iv.StartedAt, &s.iv.EndedAt, &s.iv.DurationSeconds,
		&s.integrity, &s.iv.LastError, &s.iv.CreatedAt, &s.iv.UpdatedAt}
}

func (s *interviewScan) finish() domain.Interview {
	s.iv.Status = domain.InterviewStatus(s.status)
	fromJSON(s.integrity, &s.iv.Integrity)
	return s.iv
}

func (d *DB) CreateInterview(ctx context.Context, iv *domain.Interview) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO interviews (id, job_id, user_id, status, attempt, is_practice,
		room_name, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		iv.ID, iv.JobID, iv.UserID, string(iv.Status), iv.Attempt, iv.IsPractice, iv.RoomName, iv.CreatedAt)
	return mapErr(err)
}

func (d *DB) GetInterview(ctx context.Context, id string) (*domain.Interview, error) {
	var s interviewScan
	if err := d.pool.QueryRow(ctx, `SELECT `+interviewCols+` FROM interviews i WHERE i.id = $1`, id).Scan(s.targets()...); err != nil {
		return nil, mapErr(err)
	}
	iv := s.finish()
	return &iv, nil
}

func (d *DB) UpdateInterview(ctx context.Context, iv *domain.Interview) error {
	tag, err := d.pool.Exec(ctx, `UPDATE interviews SET status=$2, is_practice=$3, room_name=$4, reset_count=$5,
		started_at=$6, ended_at=$7, duration_seconds=$8, integrity=$9, last_error=$10, updated_at=now()
		WHERE id=$1`, iv.ID, string(iv.Status), iv.IsPractice, iv.RoomName, iv.ResetCount, iv.StartedAt,
		iv.EndedAt, iv.DurationSeconds, toJSON(iv.Integrity), iv.LastError)
	if err == nil && tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return err
}

func (d *DB) CountCompletedAttempts(ctx context.Context, userID, jobID string) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM interviews WHERE user_id = $1 AND job_id = $2
		AND status IN ('completed', 'scored', 'failed')`, userID, jobID).Scan(&n)
	return n, err
}

func (d *DB) CountAttempts(ctx context.Context, userID, jobID string) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM interviews WHERE user_id = $1 AND job_id = $2`, userID, jobID).Scan(&n)
	return n, err
}

const summarySelect = `SELECT ` + interviewCols + `, j.title, j.slug, j.category, j.skills, j.division, j.level,
	u.full_name, u.email, sc.overall, COALESCE(sc.recommendation, ''), sc.data, sc.technical, sc.communication,
	COALESCE((SELECT r.decision FROM reviews r WHERE r.interview_id = i.id ORDER BY r.created_at DESC LIMIT 1), '')
	FROM interviews i
	JOIN jobs j ON j.id = i.job_id
	JOIN users u ON u.id = i.user_id
	LEFT JOIN scores sc ON sc.interview_id = i.id`

func scanSummary(rows pgx.Rows, staff bool) (domain.InterviewSummary, error) {
	var s interviewScan
	var out domain.InterviewSummary
	var skills, data []byte
	targets := append(s.targets(), &out.JobTitle, &out.JobSlug, &out.JobCategory, &skills, &out.JobDivision,
		&out.JobLevel, &out.CandidateName, &out.CandidateEmail, &out.Overall, &out.Recommendation, &data,
		&out.Technical, &out.Communication, &out.Decision)
	if err := rows.Scan(targets...); err != nil {
		return out, err
	}
	out.Interview = s.finish()
	fromJSON(skills, &out.JobSkills)
	if len(data) > 0 {
		var sc domain.Score
		fromJSON(data, &sc)
		if staff {
			out.Summary = sc.RecruiterSummary
		} else {
			out.Summary = sc.CandidateSummary
			out.SummaryByLang = summariesByLang(&sc)
		}
	}
	return out, nil
}

func (d *DB) ListUserInterviews(ctx context.Context, userID string) ([]domain.InterviewSummary, error) {
	rows, err := d.pool.Query(ctx, summarySelect+` WHERE i.user_id = $1 ORDER BY i.created_at DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.InterviewSummary
	for rows.Next() {
		s, err := scanSummary(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) AppendTurns(ctx context.Context, interviewID string, turns []domain.Turn) error {
	if len(turns) == 0 {
		return nil
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var next int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(seq), 0) + 1 FROM transcript_turns WHERE interview_id = $1`, interviewID).Scan(&next); err != nil {
		return err
	}
	for i, t := range turns {
		if _, err := tx.Exec(ctx, `INSERT INTO transcript_turns (interview_id, seq, speaker, text, start_ms, end_ms, question_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, interviewID, next+i, t.Speaker, t.Text, t.StartMs, t.EndMs, t.QuestionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (d *DB) ListTurns(ctx context.Context, interviewID string) ([]domain.Turn, error) {
	rows, err := d.pool.Query(ctx, `SELECT seq, speaker, text, start_ms, end_ms, question_id
		FROM transcript_turns WHERE interview_id = $1 ORDER BY seq`, interviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Turn
	for rows.Next() {
		var t domain.Turn
		if err := rows.Scan(&t.Seq, &t.Speaker, &t.Text, &t.StartMs, &t.EndMs, &t.QuestionID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) ClearTurns(ctx context.Context, interviewID string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM transcript_turns WHERE interview_id = $1`, interviewID)
	return err
}

// ---------- Scores and reviews ----------

func dimensionRating(s *domain.Score, key string) float64 {
	for _, d := range s.Dimensions {
		if d.Key == key {
			return d.Rating
		}
	}
	return 0
}

func (d *DB) SaveScore(ctx context.Context, s *domain.Score) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO scores (interview_id, overall, technical, communication, recommendation, data, model, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (interview_id) DO UPDATE SET overall=EXCLUDED.overall, technical=EXCLUDED.technical,
		communication=EXCLUDED.communication, recommendation=EXCLUDED.recommendation, data=EXCLUDED.data,
		model=EXCLUDED.model, created_at=EXCLUDED.created_at`,
		s.InterviewID, s.Overall, dimensionRating(s, "technical"), dimensionRating(s, "communication"),
		s.Recommendation, toJSON(s), s.Model, s.CreatedAt)
	return err
}

func (d *DB) GetScore(ctx context.Context, interviewID string) (*domain.Score, error) {
	var data []byte
	if err := d.pool.QueryRow(ctx, `SELECT data FROM scores WHERE interview_id = $1`, interviewID).Scan(&data); err != nil {
		return nil, mapErr(err)
	}
	var s domain.Score
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) AddReview(ctx context.Context, r *domain.Review) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO reviews (id, interview_id, reviewer_id, decision, notes, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, r.ID, r.InterviewID, r.ReviewerID, r.Decision, r.Notes, r.CreatedAt)
	return mapErr(err)
}

func (d *DB) ListReviews(ctx context.Context, interviewID string) ([]domain.Review, error) {
	rows, err := d.pool.Query(ctx, `SELECT r.id::text, r.interview_id::text, COALESCE(r.reviewer_id::text, ''),
		COALESCE(u.full_name, 'Deleted user'), r.decision, r.notes, r.created_at
		FROM reviews r LEFT JOIN users u ON u.id = r.reviewer_id
		WHERE r.interview_id = $1 ORDER BY r.created_at DESC`, interviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Review
	for rows.Next() {
		var r domain.Review
		if err := rows.Scan(&r.ID, &r.InterviewID, &r.ReviewerID, &r.Reviewer, &r.Decision, &r.Notes, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) ListResults(ctx context.Context, f domain.ResultFilter) ([]domain.InterviewSummary, int, error) {
	where := []string{"i.status <> 'pending'"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.JobID != "" {
		add("i.job_id = ?", f.JobID)
	}
	if f.Status != "" {
		add("i.status = ?", f.Status)
	}
	if f.Recommendation != "" {
		add("sc.recommendation = ?", f.Recommendation)
	}
	if f.MinOverall > 0 {
		add("sc.overall >= ?", f.MinOverall)
	}
	if f.MinTechnical > 0 {
		add("sc.technical >= ?", f.MinTechnical)
	}
	if !f.IncludePractice {
		where = append(where, "i.is_practice = false")
	}
	if f.Decision != "" {
		if f.Decision == "none" {
			where = append(where, "NOT EXISTS (SELECT 1 FROM reviews r WHERE r.interview_id = i.id)")
		} else {
			add("(SELECT r.decision FROM reviews r WHERE r.interview_id = i.id ORDER BY r.created_at DESC LIMIT 1) = ?", f.Decision)
		}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(q)+"%", q)
		n := len(args)
		where = append(where, fmt.Sprintf(`(u.full_name ILIKE $%d OR u.email ILIKE $%d OR EXISTS (
			SELECT 1 FROM transcript_turns t WHERE t.interview_id = i.id
			AND to_tsvector('simple', t.text) @@ plainto_tsquery('simple', $%d)))`, n-1, n-1, n))
	}
	order := "i.created_at DESC"
	switch f.Sort {
	case "-overall":
		order = "sc.overall DESC NULLS LAST, i.created_at DESC"
	case "overall":
		order = "sc.overall ASC NULLS LAST, i.created_at DESC"
	case "date":
		order = "i.created_at ASC"
	}
	cond := " WHERE " + strings.Join(where, " AND ")

	var total int
	countSQL := `SELECT count(*) FROM interviews i JOIN users u ON u.id = i.user_id
		LEFT JOIN scores sc ON sc.interview_id = i.id` + cond
	if err := d.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := f.Limit, f.Offset
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	args = append(args, limit, offset)
	rows, err := d.pool.Query(ctx, summarySelect+cond+fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []domain.InterviewSummary
	for rows.Next() {
		s, err := scanSummary(rows, true)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

func (d *DB) Stats(ctx context.Context) (*domain.Stats, error) {
	var s domain.Stats
	err := d.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM jobs WHERE status = 'open'),
		(SELECT count(*) FROM users WHERE role = 'candidate'),
		(SELECT count(*) FROM interviews WHERE status <> 'pending' AND is_practice = false),
		(SELECT count(*) FROM interviews WHERE status = 'scored' AND is_practice = false),
		(SELECT count(*) FROM interviews i WHERE i.status = 'scored' AND i.is_practice = false
			AND NOT EXISTS (SELECT 1 FROM reviews r WHERE r.interview_id = i.id)),
		COALESCE((SELECT round(avg(sc.overall))::int FROM scores sc JOIN interviews i ON i.id = sc.interview_id
			WHERE i.is_practice = false), 0)`).Scan(
		&s.OpenJobs, &s.Candidates, &s.InterviewsTotal, &s.InterviewsScored, &s.AwaitingReview, &s.AverageScore)
	return &s, err
}

// ---------- Queue ----------

func (d *DB) Enqueue(ctx context.Context, kind, payload string, runAfter time.Time) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO job_queue (id, kind, payload, run_after) VALUES (gen_random_uuid(), $1, $2, $3)`,
		kind, payload, runAfter)
	return err
}

// ClaimJob takes the next ready job. FOR UPDATE SKIP LOCKED lets several workers
// share the queue without picking the same job. Jobs stuck "running" for 10
// minutes (a crashed worker) become claimable again.
func (d *DB) ClaimJob(ctx context.Context) (*domain.QueueJob, error) {
	var j domain.QueueJob
	var payload []byte
	err := d.pool.QueryRow(ctx, `UPDATE job_queue SET status = 'running', attempts = attempts + 1, locked_at = now()
		WHERE id = (
			SELECT id FROM job_queue
			WHERE (status = 'pending' AND run_after <= now())
			   OR (status = 'running' AND locked_at < now() - interval '10 minutes')
			ORDER BY run_after LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id::text, kind, payload, attempts`).Scan(&j.ID, &j.Kind, &payload, &j.Attempts)
	if err != nil {
		return nil, mapErr(err)
	}
	j.Payload = string(payload)
	return &j, nil
}

func (d *DB) CompleteJob(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `UPDATE job_queue SET status = 'done', locked_at = NULL WHERE id = $1`, id)
	return err
}

func (d *DB) FailJob(ctx context.Context, id string, errMsg string, retryAt *time.Time) error {
	if retryAt != nil {
		_, err := d.pool.Exec(ctx, `UPDATE job_queue SET status = 'pending', locked_at = NULL, last_error = $2,
			run_after = $3 WHERE id = $1`, id, errMsg, *retryAt)
		return err
	}
	_, err := d.pool.Exec(ctx, `UPDATE job_queue SET status = 'failed', locked_at = NULL, last_error = $2 WHERE id = $1`, id, errMsg)
	return err
}

func (d *DB) Audit(ctx context.Context, e domain.AuditEntry) error {
	var uid any
	if e.UserID != "" {
		uid = e.UserID
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO audit_logs (user_id, action, entity, entity_id, ip, detail)
		VALUES ($1,$2,$3,$4,$5,$6)`, uid, e.Action, e.Entity, e.EntityID, e.IP, toJSON(e.Detail))
	return err
}

var _ store.Store = (*DB)(nil)

func summariesByLang(sc *domain.Score) map[string]string {
	m := map[string]string{}
	for _, l := range domain.FeedbackLanguages {
		m[l] = sc.CandidateFor(l).Summary
	}
	return m
}
