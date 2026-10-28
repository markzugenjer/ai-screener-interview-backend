package security

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	h, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Fatalf("unexpected format %q", h)
	}
	if !VerifyPassword("rahasia123", h) {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword("rahasia124", h) || VerifyPassword("", h) || VerifyPassword("rahasia123", "garbage") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("rahasia123")
	if h == h2 {
		t.Fatal("salts must differ")
	}
}

func TestEncryptor(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	e, err := NewEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := e.EncryptString("+6281234567890", "phone:u1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ct, "6281234567890") {
		t.Fatal("ciphertext leaks plaintext")
	}
	pt, err := e.DecryptString(ct, "phone:u1")
	if err != nil || pt != "+6281234567890" {
		t.Fatalf("roundtrip failed: %q %v", pt, err)
	}
	if _, err := e.DecryptString(ct, "phone:u2"); err == nil {
		t.Fatal("ciphertext must be bound to its owner")
	}
	raw, _ := e.Encrypt([]byte("hello"), nil)
	raw[len(raw)-1] ^= 1
	if _, err := e.Decrypt(raw, nil); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	other := make([]byte, 32)
	_, _ = rand.Read(other)
	e2, _ := NewEncryptor(other)
	if _, err := e2.DecryptString(ct, "phone:u1"); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestNormalizeWhatsApp(t *testing.T) {
	ok := map[string]string{
		"0812-3456-7890":   "+6281234567890",
		"081234567890":     "+6281234567890",
		"62 812 3456 7890": "+6281234567890",
		"+62 812 3456 789": "+628123456789",
		"+1 415 555 2671":  "+14155552671",
	}
	for in, want := range ok {
		got, err := NormalizeWhatsApp(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "12345", "021 555 1234", "0712345678", "abc", "08123"} {
		if _, err := NormalizeWhatsApp(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if m := MaskPhone("+6281234567890"); m != "+62812****7890" {
		t.Errorf("mask: %q", m)
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := map[string]bool{"short1": false, "nodigitshere": false, "12345678": false, "goodpass1": true}
	for pw, ok := range cases {
		if got := PasswordProblem(pw, "a@b.co") == ""; got != ok {
			t.Errorf("%q: ok=%v want %v", pw, got, ok)
		}
	}
}

func TestValidators(t *testing.T) {
	for _, e := range []string{"budi@mail.com", "a.b+c@sub.example.id"} {
		if !ValidEmail(e) {
			t.Errorf("%q should be valid", e)
		}
	}
	for _, e := range []string{"", "budi", "budi@", "Budi <budi@mail.com>", "budi@localhost"} {
		if ValidEmail(e) {
			t.Errorf("%q should be invalid", e)
		}
	}
	if !ValidFullName("Devan Tritama") || !ValidFullName("Siti Nur'aini") || ValidFullName("<script>") || ValidFullName("a") {
		t.Error("name validation wrong")
	}
	if got := SafeFilename("../../etc/passwd<>.pdf"); got != "passwd.pdf" {
		t.Errorf("safe filename: %q", got)
	}
}

func TestSniffResume(t *testing.T) {
	if m, ext, err := SniffResume([]byte("%PDF-1.7 rest")); err != nil || m != MimePDF || ext != ".pdf" {
		t.Fatal("pdf not detected")
	}
	if _, _, err := SniffResume([]byte("<html><script>")); err == nil {
		t.Fatal("html accepted as resume")
	}
	if _, _, err := SniffResume([]byte("PK\x03\x04garbage")); err == nil {
		t.Fatal("broken zip accepted")
	}
	if _, _, err := SniffResume(bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 4)); err == nil {
		t.Fatal("png accepted")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(60, 3)
	for i := 0; i < 3; i++ {
		if ok, _ := rl.Allow("ip"); !ok {
			t.Fatalf("request %d should pass", i)
		}
	}
	if ok, wait := rl.Allow("ip"); ok || wait <= 0 {
		t.Fatal("burst exceeded but allowed")
	}
	if ok, _ := rl.Allow("other-ip"); !ok {
		t.Fatal("limits must be per key")
	}
}

func TestEqualSecret(t *testing.T) {
	if !EqualSecret("abc", "abc") || EqualSecret("abc", "abd") || EqualSecret("", "") {
		t.Fatal("EqualSecret wrong")
	}
}
