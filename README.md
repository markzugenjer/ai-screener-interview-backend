# AI Screener Interview — Backend

Go API, scoring worker and TypeScript voice agent for **Nara**, an AI voice interviewer that runs
first-round screening interviews in Bahasa Indonesia, scores them, and lets recruiters filter results.

Frontend: [ai-screener-interview-frontend](https://github.com/markzugenjer/ai-screener-interview-frontend)

```
Browser ──► Next.js (/api/v1 proxy) ──► Go API ──► PostgreSQL
   │                                     ▲   │
   └── WebRTC audio ──► LiveKit ──► Voice agent (Node)   └─► job_queue ──► Go worker ──► Claude (scoring)
                                    Deepgram STT · Claude · ElevenLabs TTS
```

| Part | Where | What it does |
| --- | --- | --- |
| API | `cmd/api` | Auth, jobs, interviews, results, LiveKit tokens, internal agent endpoints |
| Worker | `cmd/worker` | Scores finished interviews from a Postgres queue (`FOR UPDATE SKIP LOCKED`) |
| Admin CLI | `cmd/admin` | Creates recruiter/admin accounts (they can't sign up publicly) |
| Voice agent | `agent/` | Runs the interview in the LiveKit room, adapts question difficulty, streams the transcript to the API |

## Quick start

Requirements: Go 1.24+, Node 20+ with pnpm, Docker (for Postgres).

```bash
go mod tidy                     # first time only: downloads pgx and creates go.sum (commit it)
cp .env.example .env            # fill in; generate secrets with: openssl rand -base64 32
cp agent/.env.example agent/.env

docker compose up -d db         # Postgres on localhost:5432 (user/pass/db: screener)
make up                         # API on :8080 + scoring worker + voice agent, all in one terminal (Ctrl+C stops all)
make up AGENT=0                 # same without the voice agent (enough for browsing, feedback and admin pages)

# Or run them separately, one per terminal:
make dev                        # API — runs migrations and seeds two demo jobs
make worker                     # scoring worker
make agent-dev                  # voice agent (needs LiveKit, Deepgram, ElevenLabs, Anthropic keys)
```

Then start the frontend (see its README) and open http://localhost:3000.

**Without a database:** set `STORE=memory` to try everything in one process (data is lost on restart; the worker runs inside the API).

**Everything in Docker:** `docker compose up --build` (API, worker, agent and Postgres).

### Recruiter accounts

Set `ADMIN_EMAIL`, `ADMIN_PASSWORD` (12+ characters) and `ADMIN_NAME` in `.env` before the first start to create an admin, then remove them. Afterwards:

```bash
make admin EMAIL=sinta@company.com NAME="Sinta Dewi"          # prompts for a password
go run ./cmd/admin promote -role admin -email sinta@company.com
```

## How an interview works

1. A candidate registers (name, email, password, WhatsApp, resume) and starts an interview for an open job.
2. `POST /api/v1/interviews/{id}/token` returns a LiveKit token. Its `roomConfig` dispatches the `ai-screener` agent into the room.
3. The agent loads the job and question bank from `/internal/interviews/{id}/context`, greets the candidate and
   uses its `next_question` tool. Difficulty adapts: strong answers move a skill up a level, two weak answers move on.
4. Transcript turns (with timing) stream to `/internal/interviews/{id}/turns`.
5. When the agent ends the call or the candidate leaves, the interview is marked `completed` and a scoring job is queued.
6. The worker computes speech metrics in code, asks Claude to grade each answer against its expected points and rate
   the holistic dimensions, then computes the weighted 0–100 score and recommendation deterministically.
7. Candidates see an encouraging summary, a breakdown and tips. Recruiters additionally see the frank summary,
   per-question evidence, transcript, integrity signals, recommendation, and can shortlist, hold or reject.

**Rules:** one reset, only in the first 2 minutes. Only the first completed attempt counts; later attempts are marked
practice and hidden from recruiters by default.

## Security

| Area | What's in place |
| --- | --- |
| Passwords | PBKDF2-HMAC-SHA256, 600,000 iterations, per-user salt; constant-time compare; same timing for unknown emails |
| Sessions | Random 256-bit token in an `HttpOnly`, `SameSite=Lax` (and `Secure` in production) cookie; only its SHA-256 is stored; rotated on login; server-side logout |
| CSRF | Double-submit token (`X-CSRF-Token` header must match the `ais_csrf` cookie) plus an `Origin` allow-list on every state-changing request |
| Encryption at rest | AES-256-GCM for WhatsApp numbers and resume files, bound to the owning user (a ciphertext copied to another row fails to decrypt) |
| Encryption in transit | Serve the site and API over HTTPS only (`COOKIE_SECURE=true` is enforced in production); HSTS header is set |
| Brute force | Per-IP rate limits (stricter on login/register), 15-minute lockout after 5 failed logins |
| Uploads | 5 MB limit, file type detected from content (PDF/DOCX only), sanitized names, random storage keys, never served inline |
| Input | Strict JSON decoding (unknown fields rejected, size-limited), validation on every field, parameterized SQL only |
| Access control | Role checks on every recruiter route; candidates can only read their own interviews (others get 404) |
| Agent endpoints | `/internal/*` needs `INTERNAL_API_SECRET` and is not exposed through the website |
| Headers | `nosniff`, `X-Frame-Options: DENY`, restrictive CSP, `no-store` caching |
| Audit log | Registrations, logins, lockouts, job changes, result views, reviews and resume downloads |
| Privacy | AI never decides alone; integrity signals are flags, never automatic rejections. Review the consent text and data retention with your legal team (UU PDP No. 27/2022). |

## API

Public: `GET /api/v1/jobs`, `GET /api/v1/jobs/{slug}`, `GET /api/v1/auth/csrf`, `POST /api/v1/auth/register` (multipart), `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`

Candidate: `GET /api/v1/auth/me`, `GET /api/v1/me/interviews`, `POST /api/v1/jobs/{slug}/interviews`, `GET /api/v1/interviews/{id}`, `POST /api/v1/interviews/{id}/token|reset|finish|events`, `GET /api/v1/interviews/{id}/feedback`

Recruiter: `GET /api/v1/admin/stats`, `GET|POST /api/v1/admin/jobs`, `GET|PUT|DELETE /api/v1/admin/jobs/{id}`, `GET /api/v1/admin/results?jobId&status&recommendation&decision&minOverall&minTechnical&q&practice&sort&page&pageSize`, `GET /api/v1/admin/results/{id}`, `POST /api/v1/admin/results/{id}/review|rescore`, `GET /api/v1/admin/candidates/{id}/resume`

Agent (shared secret): `GET /internal/interviews/{id}/context`, `POST /internal/interviews/{id}/turns`, `POST /internal/interviews/{id}/complete`

## Tests

```bash
make test        # security, scoring, LiveKit token, JSON parsing and a full API flow test
make agent-test  # adaptive question plan and transcript batching
```

## Design notes

- **Minimal dependencies.** Routing, crypto, JWT signing for LiveKit and the Claude client use Go's standard library. The only third-party Go module is `pgx`, the Postgres driver. Migrations are plain SQL files in `internal/store/postgres/migrations`, applied in order at startup.
- **Postgres queue instead of Redis.** At 10–50 interviews a month, a `job_queue` table with `SKIP LOCKED` is plenty and removes a moving part.
- **Voice agent in TypeScript.** LiveKit's agent framework exists for Node.js and Python, not Go. The agent stays thin; all data and logic live in Go.
