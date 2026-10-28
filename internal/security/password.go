// Package security holds password hashing, encryption, tokens, validation and rate limiting.
package security

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Passwords are hashed with PBKDF2-HMAC-SHA256 (Go standard library), using the
// OWASP 2023+ recommended 600,000 iterations and a 16-byte random salt per user.
// Format: pbkdf2-sha256$<iterations>$<salt b64>$<hash b64>
const (
	pbkdf2Iterations = 600_000
	saltLen          = 16
	keyLen           = 32
)

var ErrBadHash = errors.New("invalid password hash format")

func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

// VerifyPassword compares in constant time. It returns false for any malformed hash.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 100_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyVerify burns the same CPU time as a real check, so login timing does not
// reveal whether an email address is registered.
func DummyVerify(password string) {
	_, _ = pbkdf2.Key(sha256.New, password, make([]byte, saltLen), pbkdf2Iterations, keyLen)
}
