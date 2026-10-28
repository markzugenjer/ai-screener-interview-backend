-- AI Screener Interview: initial schema.
-- Personal data (WhatsApp numbers, resume files) is encrypted in the application
-- with AES-256-GCM before it reaches the database or disk.

CREATE TABLE users (
    id             uuid PRIMARY KEY,
    role           text NOT NULL DEFAULT 'candidate' CHECK (role IN ('candidate', 'recruiter', 'admin')),
    full_name      text NOT NULL,
    email          text NOT NULL,
    password_hash  text NOT NULL,
    phone_enc      text NOT NULL DEFAULT '',
    resume_key     text NOT NULL DEFAULT '',
    resume_name    text NOT NULL DEFAULT '',
    resume_mime    text NOT NULL DEFAULT '',
    failed_logins  int NOT NULL DEFAULT 0,
    locked_until   timestamptz,
    last_login_at  timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE sessions (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    user_agent  text NOT NULL DEFAULT '',
    ip          text NOT NULL DEFAULT '',
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE jobs (
    id                uuid PRIMARY KEY,
    title             text NOT NULL,
    slug              text NOT NULL UNIQUE,
    division          text NOT NULL DEFAULT 'Engineering',
    category          text NOT NULL DEFAULT 'Technical',
    level             text NOT NULL DEFAULT 'junior' CHECK (level IN ('junior', 'mid', 'senior')),
    location          text NOT NULL DEFAULT '',
    employment_type   text NOT NULL DEFAULT 'Full-time',
    description       text NOT NULL DEFAULT '',
    requirements      jsonb NOT NULL DEFAULT '[]',
    skills            jsonb NOT NULL DEFAULT '[]',
    duration_minutes  int NOT NULL DEFAULT 20 CHECK (duration_minutes BETWEEN 5 AND 60),
    status            text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed')),
    weights           jsonb NOT NULL,
    created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_status_idx ON jobs (status);

CREATE TABLE questions (
    id               uuid PRIMARY KEY,
    job_id           uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    skill            text NOT NULL,
    difficulty       int NOT NULL CHECK (difficulty BETWEEN 1 AND 3),
    type             text NOT NULL DEFAULT 'technical' CHECK (type IN ('technical', 'scenario', 'soft')),
    text             text NOT NULL,
    expected_points  jsonb NOT NULL DEFAULT '[]',
    follow_ups       jsonb NOT NULL DEFAULT '[]',
    sort             int NOT NULL DEFAULT 0
);
CREATE INDEX questions_job_idx ON questions (job_id, sort);

CREATE TABLE interviews (
    id                uuid PRIMARY KEY,
    job_id            uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    user_id           uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status            text NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'in_progress', 'completed', 'scored', 'failed', 'abandoned')),
    attempt           int NOT NULL DEFAULT 1,
    is_practice       boolean NOT NULL DEFAULT false,
    room_name         text NOT NULL,
    reset_count       int NOT NULL DEFAULT 0,
    started_at        timestamptz,
    ended_at          timestamptz,
    duration_seconds  int NOT NULL DEFAULT 0,
    integrity         jsonb NOT NULL DEFAULT '{}',
    last_error        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX interviews_user_idx ON interviews (user_id, created_at DESC);
CREATE INDEX interviews_job_status_idx ON interviews (job_id, status);

CREATE TABLE transcript_turns (
    id            bigserial PRIMARY KEY,
    interview_id  uuid NOT NULL REFERENCES interviews (id) ON DELETE CASCADE,
    seq           int NOT NULL,
    speaker       text NOT NULL CHECK (speaker IN ('ai', 'candidate')),
    text          text NOT NULL,
    start_ms      int NOT NULL DEFAULT 0,
    end_ms        int NOT NULL DEFAULT 0,
    question_id   text NOT NULL DEFAULT ''
);
CREATE INDEX transcript_turns_interview_idx ON transcript_turns (interview_id, seq);
CREATE INDEX transcript_turns_text_idx ON transcript_turns USING gin (to_tsvector('simple', text));

CREATE TABLE scores (
    interview_id       uuid PRIMARY KEY REFERENCES interviews (id) ON DELETE CASCADE,
    overall            int NOT NULL CHECK (overall BETWEEN 0 AND 100),
    technical          double precision NOT NULL DEFAULT 0,
    communication      double precision NOT NULL DEFAULT 0,
    recommendation     text NOT NULL,
    data               jsonb NOT NULL,
    model              text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scores_overall_idx ON scores (overall);
CREATE INDEX scores_technical_idx ON scores (technical);

CREATE TABLE reviews (
    id            uuid PRIMARY KEY,
    interview_id  uuid NOT NULL REFERENCES interviews (id) ON DELETE CASCADE,
    reviewer_id   uuid REFERENCES users (id) ON DELETE SET NULL,
    decision      text NOT NULL CHECK (decision IN ('shortlist', 'reject', 'hold')),
    notes         text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_interview_idx ON reviews (interview_id, created_at DESC);

CREATE TABLE job_queue (
    id          uuid PRIMARY KEY,
    kind        text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}',
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts    int NOT NULL DEFAULT 0,
    run_after   timestamptz NOT NULL DEFAULT now(),
    locked_at   timestamptz,
    last_error  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX job_queue_ready_idx ON job_queue (status, run_after);

CREATE TABLE audit_logs (
    id          bigserial PRIMARY KEY,
    user_id     uuid,
    action      text NOT NULL,
    entity      text NOT NULL DEFAULT '',
    entity_id   text NOT NULL DEFAULT '',
    ip          text NOT NULL DEFAULT '',
    detail      jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC);
