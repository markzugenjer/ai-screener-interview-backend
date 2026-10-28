// Command api runs the HTTP API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/api"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/app"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/config"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/llm"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/security"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/seed"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/storage"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := app.Logger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := app.OpenStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()

	if os.Getenv("SEED_DEMO") != "false" {
		if added, err := seed.Demo(ctx, st); err != nil {
			log.Error("seed demo jobs", "err", err)
		} else if added {
			log.Info("seeded demo jobs and question bank")
		}
	}
	if err := app.BootstrapAdmin(ctx, st, log); err != nil {
		log.Error("bootstrap admin", "err", err)
	}

	enc, err := security.NewEncryptor(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	files, err := storage.NewLocal(cfg.StorageDir, enc)
	if err != nil {
		return err
	}

	// The in-memory store lives inside this process, so its worker must too.
	if cfg.RunWorkerInAPI || cfg.Store == "memory" {
		w := &worker.Worker{Store: st, LLM: llm.NewAnthropic(cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.AnthropicBaseURL),
			Language: cfg.FeedbackLanguage, Log: log.With("component", "worker")}
		go w.Run(ctx)
	}
	if cfg.AnthropicAPIKey == "" {
		log.Warn("ANTHROPIC_API_KEY not set: interviews will be saved but not scored")
	}
	if !cfg.LiveKitConfigured() {
		log.Warn("LiveKit not configured: candidates cannot join voice interviews")
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(cfg, st, enc, files, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env, "store", cfg.Store)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}
