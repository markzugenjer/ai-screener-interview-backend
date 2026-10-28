// Package app wires configuration, storage and shared services for the binaries.
package app

import (
	"context"
	"log/slog"
	"os"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/config"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store/memory"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store/postgres"
)

func Logger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if cfg != nil && cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// OpenStore connects to the configured store and applies migrations.
func OpenStore(ctx context.Context, cfg *config.Config, log *slog.Logger) (store.Store, error) {
	if cfg.Store == "memory" {
		log.Warn("using in-memory store: all data is lost on restart (development only)")
		return memory.New(), nil
	}
	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
