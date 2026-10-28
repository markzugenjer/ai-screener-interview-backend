// Package llm is a minimal client for the Anthropic Messages API (Claude).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrNotConfigured = errors.New("ANTHROPIC_API_KEY is not set")

type Completer interface {
	Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error)
	Model() string
}

type Anthropic struct {
	apiKey string
	model  string
	http   *http.Client
	base   string
}

func NewAnthropic(apiKey, model, baseURL string) *Anthropic {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &Anthropic{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 120 * time.Second},
		base:   strings.TrimRight(baseURL, "/"),
	}
}

func (a *Anthropic) Model() string { return a.model }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type response struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *Anthropic) Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	if a.apiKey == "" {
		return "", ErrNotConfigured
	}
	body, _ := json.Marshal(request{
		Model: a.model, MaxTokens: maxTokens, System: system,
		Messages: []message{{Role: "user", Content: prompt}},
	})
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", a.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		res, err := a.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		var out response
		_ = json.Unmarshal(raw, &out)
		if res.StatusCode == 429 || res.StatusCode >= 500 {
			lastErr = fmt.Errorf("anthropic: status %d", res.StatusCode)
			continue
		}
		if res.StatusCode != http.StatusOK {
			msg := http.StatusText(res.StatusCode)
			if out.Error != nil {
				msg = out.Error.Message
			}
			return "", fmt.Errorf("anthropic: %d %s", res.StatusCode, msg)
		}
		var sb strings.Builder
		for _, c := range out.Content {
			if c.Type == "text" {
				sb.WriteString(c.Text)
			}
		}
		return sb.String(), nil
	}
	return "", lastErr
}

// ExtractJSON pulls the first top-level JSON object out of a model reply,
// tolerating ```json fences or a sentence before it.
func ExtractJSON(s string) (string, error) {
	start := strings.Index(s, "{")
	if start < 0 {
		return "", errors.New("no JSON object in model reply")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("unterminated JSON object in model reply")
}
