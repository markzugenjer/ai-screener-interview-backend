// Command worker processes background jobs (interview scoring) from the Postgres queue.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/app"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/config"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/llm"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	if cfg.Store == "memory" {
		fmt.Fprintln(os.Stderr, "fatal:", errors.New("the standalone worker needs STORE=postgres (the memory store runs its worker inside the API)"))
		os.Exit(1)
	}
	log := app.Logger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := app.OpenStore(ctx, cfg, log)
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if cfg.AnthropicAPIKey == "" {
		log.Warn("ANTHROPIC_API_KEY not set: scoring jobs will fail until it is configured")
	}
	w := &worker.Worker{Store: st, LLM: llm.NewAnthropic(cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.AnthropicBaseURL),
		Language: cfg.FeedbackLanguage, Log: log}
	w.Run(ctx)
}
