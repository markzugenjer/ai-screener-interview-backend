package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// Encryptor encrypts personal data at rest (WhatsApp numbers, resume files)
// with AES-256-GCM. Each value gets a fresh random 12-byte nonce.
// Ciphertext layout: version(1) | nonce(12) | sealed data + GCM tag(16).
// The version byte lets us rotate keys later without breaking old rows.
type Encryptor struct {
	aead cipher.AEAD
}

const cipherVersion byte = 1

var ErrDecrypt = errors.New("decryption failed")

func NewEncryptor(key []byte) (*Encryptor, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Encryptor{aead: aead}, nil
}

// Encrypt seals plaintext. aad ("additional authenticated data") binds the
// ciphertext to its owner, e.g. the user id, so a value copied to another row fails to decrypt.
func (e *Encryptor) Encrypt(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+e.aead.Overhead())
	out = append(out, cipherVersion)
	out = append(out, nonce...)
	return e.aead.Seal(out, nonce, plaintext, aad), nil
}

func (e *Encryptor) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	ns := e.aead.NonceSize()
	if len(ciphertext) < 1+ns+e.aead.Overhead() || ciphertext[0] != cipherVersion {
		return nil, ErrDecrypt
	}
	nonce := ciphertext[1 : 1+ns]
	pt, err := e.aead.Open(nil, nonce, ciphertext[1+ns:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

func (e *Encryptor) EncryptString(s string, aad string) (string, error) {
	b, err := e.Encrypt([]byte(s), []byte(aad))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func (e *Encryptor) DecryptString(s string, aad string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", ErrDecrypt
	}
	pt, err := e.Decrypt(b, []byte(aad))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// RandomToken returns n random bytes encoded as URL-safe base64 (no padding).
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken stores only a SHA-256 of session tokens, so a database leak
// does not leak usable login sessions.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// EqualSecret compares two secrets in constant time.
func EqualSecret(a, b string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	ha := hmac.New(sha256.New, []byte("cmp"))
	ha.Write([]byte(a))
	hb := hmac.New(sha256.New, []byte("cmp"))
	hb.Write([]byte(b))
	return subtle.ConstantTimeCompare(ha.Sum(nil), hb.Sum(nil)) == 1
}
