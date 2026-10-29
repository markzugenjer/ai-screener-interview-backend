package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/config"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CreateStaff creates a recruiter or admin account. Staff accounts can never be
// created from the public register page; only through this function (CLI or bootstrap).
func CreateStaff(ctx context.Context, st store.Store, name, email, password string, role domain.Role) (*domain.User, error) {
	email = security.NormalizeEmail(email)
	if !security.ValidEmail(email) {
		return nil, errors.New("invalid email")
	}
	if !security.ValidFullName(name) {
		return nil, errors.New("invalid name")
	}
	if p := security.PasswordProblem(password, email); p != "" {
		return nil, errors.New(p)
	}
	if len(password) < 12 {
		return nil, errors.New("staff passwords must be at least 12 characters")
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &domain.User{ID: uuid(), Role: role, FullName: name, Email: email, PasswordHash: hash, CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// BootstrapAdmin creates the first admin from ADMIN_EMAIL / ADMIN_PASSWORD /
// ADMIN_NAME when that email does not exist yet. Remove the variables afterwards.
func BootstrapAdmin(ctx context.Context, st store.Store, log *slog.Logger) error {
	email := config.Env("ADMIN_EMAIL")
	pw := config.Env("ADMIN_PASSWORD")
	if email == "" || pw == "" {
		return nil
	}
	if _, err := st.GetUserByEmail(ctx, email); err == nil {
		return nil
	}
	name := config.Env("ADMIN_NAME")
	if name == "" {
		name = "Admin"
	}
	if _, err := CreateStaff(ctx, st, name, email, pw, domain.RoleAdmin); err != nil {
		return err
	}
	log.Info("created bootstrap admin account", "email", email)
	return nil
}
