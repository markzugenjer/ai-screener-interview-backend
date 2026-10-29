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

	if cfg.SeedDemo {
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
	var files storage.Files
	if cfg.VCSStorageURL != "" {
		if files, err = storage.NewVCS(cfg.VCSStorageURL, cfg.VCSStorageToken, enc); err != nil {
			return err
		}
		log.Info("file storage: VCS object storage")
	} else if files, err = storage.NewLocal(cfg.StorageDir, enc); err != nil {
		return err
	}

	w := &worker.Worker{Store: st, LLM: llm.NewAnthropic(cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.AnthropicBaseURL),
		Language: cfg.FeedbackLanguage, Log: log.With("component", "worker")}
	handler := api.New(cfg, st, enc, files, log)
	if cfg.AnthropicAPIKey != "" {
		handler.WithTranslator(llm.NewAnthropic(cfg.AnthropicAPIKey, cfg.TranslateModel, cfg.AnthropicBaseURL))
	}
	writeTimeout := 60 * time.Second
	switch {
	case cfg.InlineJobs:
		// Request-based host: score inside the requests that wait for it.
		handler.WithInlineJobs(w)
		writeTimeout = 5 * time.Minute
		log.Info("jobs run inline (INLINE_JOBS=true)")
	case cfg.RunWorkerInAPI || cfg.Store == "memory":
		// The in-memory store lives inside this process, so its worker must too.
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
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      writeTimeout,
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
