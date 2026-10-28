package security

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FieldErrors maps a form field to a human-readable problem.
type FieldErrors map[string]string

func (f FieldErrors) Any() bool { return len(f) > 0 }

func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func ValidEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}

var nameRe = regexp.MustCompile(`^[\p{L}\p{M}' .\-]+$`)

func ValidFullName(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 2 && n <= 100 && nameRe.MatchString(s)
}

// PasswordProblem returns "" when the password is acceptable.
// Policy: 8–128 characters, at least one letter and one number, not the email.
func PasswordProblem(pw, email string) string {
	n := utf8.RuneCountInString(pw)
	if n < 8 {
		return "Password must be at least 8 characters."
	}
	if n > 128 {
		return "Password must be at most 128 characters."
	}
	var letter, digit bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	if !letter || !digit {
		return "Password must contain at least one letter and one number."
	}
	if email != "" && strings.EqualFold(pw, email) {
		return "Password must not be your email address."
	}
	return ""
}

var nonDigit = regexp.MustCompile(`\D`)

// NormalizeWhatsApp converts Indonesian numbers like "0812-3456-7890",
// "62812..." or "+62 812..." to E.164 "+62812...". Other countries are
// accepted when given in full "+<country code>" form.
func NormalizeWhatsApp(s string) (string, error) {
	s = strings.TrimSpace(s)
	plus := strings.HasPrefix(s, "+")
	d := nonDigit.ReplaceAllString(s, "")
	switch {
	case strings.HasPrefix(d, "62"):
		d = d[2:]
	case strings.HasPrefix(d, "0") && !plus:
		d = d[1:]
	case plus:
		if len(d) < 8 || len(d) > 15 {
			return "", errors.New("invalid number")
		}
		return "+" + d, nil
	}
	// Indonesian mobile numbers start with 8 and have 9–12 digits after the country code.
	if !strings.HasPrefix(d, "8") || len(d) < 9 || len(d) > 12 {
		return "", errors.New("enter a valid Indonesian WhatsApp number, e.g. 0812 3456 7890")
	}
	return "+62" + d, nil
}

// MaskPhone hides the middle digits for display: +62812****7890.
func MaskPhone(p string) string {
	if len(p) < 11 {
		return "****"
	}
	return p[:6] + strings.Repeat("*", len(p)-10) + p[len(p)-4:]
}

// Resume types we accept, detected from the file's bytes, not its name.
const (
	MimePDF  = "application/pdf"
	MimeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

// SniffResume checks the real content type. Only PDF and DOCX are accepted;
// legacy .doc files and anything else (images, executables, HTML) are rejected.
func SniffResume(data []byte) (mime string, ext string, err error) {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return MimePDF, ".pdf", nil
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if zerr != nil {
			return "", "", errors.New("the file is not a valid DOCX document")
		}
		for _, f := range zr.File {
			if f.Name == "word/document.xml" {
				return MimeDOCX, ".docx", nil
			}
		}
		return "", "", errors.New("the file is not a valid DOCX document")
	}
	return "", "", errors.New("resume must be a PDF or DOCX file")
}

// SafeFilename keeps a display name readable without path tricks or control characters.
func SafeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" ._-()", r) {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = "resume"
	}
	if utf8.RuneCountInString(out) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
}
