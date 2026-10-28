// Package livekit creates LiveKit access tokens (JWT, HS256) using only the
// standard library. A token lets the candidate's browser join one room, and
// its roomConfig asks LiveKit to dispatch our voice agent into that room.
package livekit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

type VideoGrant struct {
	Room           string `json:"room"`
	RoomJoin       bool   `json:"roomJoin"`
	CanPublish     bool   `json:"canPublish"`
	CanSubscribe   bool   `json:"canSubscribe"`
	CanPublishData bool   `json:"canPublishData"`
}

type AgentDispatch struct {
	AgentName string `json:"agentName"`
	Metadata  string `json:"metadata,omitempty"`
}

type RoomConfig struct {
	Agents           []AgentDispatch `json:"agents,omitempty"`
	EmptyTimeout     uint32          `json:"emptyTimeout,omitempty"`
	DepartureTimeout uint32          `json:"departureTimeout,omitempty"`
	MaxParticipants  uint32          `json:"maxParticipants,omitempty"`
}

type claims struct {
	Issuer     string      `json:"iss"`
	Subject    string      `json:"sub"`
	NotBefore  int64       `json:"nbf"`
	Expires    int64       `json:"exp"`
	Name       string      `json:"name,omitempty"`
	Metadata   string      `json:"metadata,omitempty"`
	Video      VideoGrant  `json:"video"`
	RoomConfig *RoomConfig `json:"roomConfig,omitempty"`
}

type TokenParams struct {
	APIKey, APISecret string
	Identity, Name    string
	Room              string
	AgentName         string
	AgentMetadata     string
	TTL               time.Duration
}

func CreateToken(p TokenParams) (string, error) {
	if p.APIKey == "" || p.APISecret == "" {
		return "", errors.New("livekit api key/secret not configured")
	}
	now := time.Now()
	c := claims{
		Issuer:    p.APIKey,
		Subject:   p.Identity,
		NotBefore: now.Add(-10 * time.Second).Unix(),
		Expires:   now.Add(p.TTL).Unix(),
		Name:      p.Name,
		Video: VideoGrant{
			Room: p.Room, RoomJoin: true, CanPublish: true, CanSubscribe: true, CanPublishData: true,
		},
		RoomConfig: &RoomConfig{
			Agents:           []AgentDispatch{{AgentName: p.AgentName, Metadata: p.AgentMetadata}},
			EmptyTimeout:     120,
			DepartureTimeout: 30,
			MaxParticipants:  2, // the candidate and the AI interviewer only
		},
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(p.APISecret))
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
