package livekit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCreateToken(t *testing.T) {
	tok, err := CreateToken(TokenParams{APIKey: "APIkey", APISecret: "s3cret", Identity: "candidate-1",
		Name: "Budi", Room: "interview-1", AgentName: "ai-screener", AgentMetadata: `{"interviewId":"1"}`, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal("not a JWT")
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Fatal("bad signature")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "APIkey" || c.Subject != "candidate-1" || c.Video.Room != "interview-1" || !c.Video.RoomJoin {
		t.Fatalf("claims: %+v", c)
	}
	if c.RoomConfig == nil || c.RoomConfig.Agents[0].AgentName != "ai-screener" || c.RoomConfig.MaxParticipants != 2 {
		t.Fatal("agent dispatch missing")
	}
	if c.Expires <= time.Now().Unix() {
		t.Fatal("token already expired")
	}
	if _, err := CreateToken(TokenParams{}); err == nil {
		t.Fatal("missing credentials must fail")
	}
}
