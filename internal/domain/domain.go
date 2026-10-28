// Package domain defines the core data types shared by the API, store and worker.
package domain

import "time"

type Role string

const (
	RoleCandidate Role = "candidate"
	RoleRecruiter Role = "recruiter"
	RoleAdmin     Role = "admin"
)

func (r Role) IsStaff() bool { return r == RoleRecruiter || r == RoleAdmin }

type User struct {
	ID           string
	Role         Role
	FullName     string
	Email        string
	PasswordHash string
	PhoneEnc     string // AES-GCM encrypted E.164 WhatsApp number (base64)
	ResumeKey    string // storage key of the encrypted resume file
	ResumeName   string
	ResumeMime   string
	FailedLogins int
	LockedUntil  *time.Time
	LastLoginAt  *time.Time
	CreatedAt    time.Time
}

type Session struct {
	ID        string
	UserID    string
	TokenHash string
	UserAgent string
	IP        string
	ExpiresAt time.Time
	CreatedAt time.Time
}

type JobStatus string

const (
	JobDraft  JobStatus = "draft"
	JobOpen   JobStatus = "open"
	JobClosed JobStatus = "closed"
)

// Weights are percentages per scoring dimension and must sum to 100.
type Weights struct {
	Technical      int `json:"technical"`
	ProblemSolving int `json:"problemSolving"`
	Communication  int `json:"communication"`
	Speech         int `json:"speech"`
	Motivation     int `json:"motivation"`
}

func DefaultWeights() Weights {
	return Weights{Technical: 40, ProblemSolving: 20, Communication: 20, Speech: 10, Motivation: 10}
}

func (w Weights) Sum() int {
	return w.Technical + w.ProblemSolving + w.Communication + w.Speech + w.Motivation
}

type Job struct {
	ID              string
	Title           string
	Slug            string
	Division        string
	Category        string // e.g. "Technical", "Behavioral"
	Level           string // junior, mid, senior
	Location        string
	EmploymentType  string
	Description     string
	Requirements    []string
	Skills          []string // tech tags shown on cards, e.g. "react", "tailwind"
	DurationMinutes int
	Status          JobStatus
	Weights         Weights
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	QuestionCount   int // filled in list queries
}

type Question struct {
	ID             string   `json:"id"`
	JobID          string   `json:"jobId"`
	Skill          string   `json:"skill"`
	Difficulty     int      `json:"difficulty"` // 1 basic, 2 intermediate, 3 advanced
	Type           string   `json:"type"`       // technical, scenario, soft
	Text           string   `json:"text"`
	ExpectedPoints []string `json:"expectedPoints"`
	FollowUps      []string `json:"followUps"`
	Sort           int      `json:"sort"`
}

type InterviewStatus string

const (
	InterviewPending    InterviewStatus = "pending"     // created, candidate not connected yet
	InterviewInProgress InterviewStatus = "in_progress" // agent and candidate in the room
	InterviewCompleted  InterviewStatus = "completed"   // finished, waiting for scoring
	InterviewScored     InterviewStatus = "scored"
	InterviewFailed     InterviewStatus = "failed"    // scoring failed, can be retried
	InterviewAbandoned  InterviewStatus = "abandoned" // left before any real answer
)

type Interview struct {
	ID              string
	JobID           string
	UserID          string
	Status          InterviewStatus
	Attempt         int
	IsPractice      bool // only the first completed attempt counts for recruiting
	RoomName        string
	ResetCount      int
	StartedAt       *time.Time
	EndedAt         *time.Time
	DurationSeconds int
	Integrity       Integrity
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Integrity collects browser signals shown to recruiters as flags, never used to auto-reject.
type Integrity struct {
	TabHidden   int `json:"tabHidden"`
	WindowBlur  int `json:"windowBlur"`
	PasteEvents int `json:"pasteEvents"`
}

type Turn struct {
	Speaker    string `json:"speaker"` // "ai" or "candidate"
	Text       string `json:"text"`
	StartMs    int    `json:"startMs"`
	EndMs      int    `json:"endMs"`
	QuestionID string `json:"questionId,omitempty"`
	Seq        int    `json:"seq"`
}

type Dimension struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Points float64  `json:"points"` // weighted points earned
	Max    int      `json:"max"`    // equals the dimension weight
	Rating float64  `json:"rating"` // raw 1–5
	Notes  []string `json:"notes"`
}

type QuestionScore struct {
	QuestionID    string   `json:"questionId"`
	Question      string   `json:"question"`
	Skill         string   `json:"skill"`
	Score         int      `json:"score"` // 1–5
	CoveredPoints []string `json:"coveredPoints"`
	MissedPoints  []string `json:"missedPoints"`
	Evidence      string   `json:"evidence"`
	Reasoning     string   `json:"reasoning"`
}

type SpeechMetrics struct {
	WordsPerMinute  float64 `json:"wordsPerMinute"`
	FillerWords     int     `json:"fillerWords"`
	LongPauses      int     `json:"longPauses"`
	CandidateWords  int     `json:"candidateWords"`
	SpeakingSeconds float64 `json:"speakingSeconds"`
	Rating          float64 `json:"rating"`
}

// FeedbackLanguages are the languages candidate feedback is written in.
var FeedbackLanguages = []string{"en", "id"}

// CandidateText is the candidate-facing feedback in one language.
type CandidateText struct {
	Summary   string              `json:"summary"`
	Strengths []string            `json:"strengths"`
	Growth    []string            `json:"growth"`
	Tips      []string            `json:"tips"`
	Notes     map[string][]string `json:"notes"` // by dimension key
}

type Score struct {
	InterviewID      string          `json:"interviewId"`
	Overall          int             `json:"overall"`
	Dimensions       []Dimension     `json:"dimensions"`
	QuestionScores   []QuestionScore `json:"questionScores"`
	Speech           SpeechMetrics   `json:"speech"`
	RecruiterSummary string          `json:"recruiterSummary"`
	CandidateSummary string          `json:"candidateSummary"`
	Strengths        []string        `json:"strengths"`
	Improvements     []string        `json:"improvements"`
	Tips             []string        `json:"tips"`
	Recommendation   string          `json:"recommendation"` // strong_yes, yes, maybe, no
	// Localized holds candidate feedback per language ("en", "id").
	Localized map[string]CandidateText `json:"localized,omitempty"`
	Model     string                   `json:"model"`
	CreatedAt time.Time                `json:"createdAt"`
}

type Review struct {
	ID          string    `json:"id"`
	InterviewID string    `json:"interviewId"`
	ReviewerID  string    `json:"reviewerId"`
	Reviewer    string    `json:"reviewer"`
	Decision    string    `json:"decision"` // shortlist, reject, hold
	Notes       string    `json:"notes"`
	CreatedAt   time.Time `json:"createdAt"`
}

// InterviewSummary is a row in the candidate's history and the recruiter's results list.
type InterviewSummary struct {
	Interview
	// SummaryByLang holds the candidate summary per language (candidate lists only).
	SummaryByLang  map[string]string
	JobTitle       string
	JobSlug        string
	JobCategory    string
	JobSkills      []string
	JobDivision    string
	JobLevel       string
	CandidateName  string
	CandidateEmail string
	Overall        *int
	Recommendation string
	Summary        string // candidate-facing summary for candidates, recruiter summary for staff
	Technical      *float64
	Communication  *float64
	Decision       string // last recruiter decision
}

type ResultFilter struct {
	JobID           string
	Status          string
	Recommendation  string
	Decision        string
	MinOverall      int
	MinTechnical    float64
	Query           string
	IncludePractice bool
	Sort            string // "-overall", "overall", "-date", "date"
	Limit           int
	Offset          int
}

type Stats struct {
	OpenJobs         int `json:"openJobs"`
	Candidates       int `json:"candidates"`
	InterviewsTotal  int `json:"interviewsTotal"`
	InterviewsScored int `json:"interviewsScored"`
	AwaitingReview   int `json:"awaitingReview"`
	AverageScore     int `json:"averageScore"`
}

type QueueJob struct {
	ID       string
	Kind     string
	Payload  string // JSON
	Attempts int
}

type AuditEntry struct {
	UserID   string
	Action   string
	Entity   string
	EntityID string
	IP       string
	Detail   map[string]any
}

// CandidateFor returns candidate feedback in lang. Scores saved before feedback
// was localized fall back to the single-language fields.
func (s *Score) CandidateFor(lang string) CandidateText {
	if ct, ok := s.Localized[lang]; ok && ct.Summary != "" {
		return ct
	}
	notes := map[string][]string{}
	for _, d := range s.Dimensions {
		notes[d.Key] = d.Notes
	}
	return CandidateText{Summary: s.CandidateSummary, Strengths: s.Strengths, Growth: s.Improvements, Tips: s.Tips, Notes: notes}
}

// NormalizeLanguage maps a request value to a supported feedback language ("" if unknown).
func NormalizeLanguage(v string) string {
	for _, l := range FeedbackLanguages {
		if v == l {
			return l
		}
	}
	return ""
}
