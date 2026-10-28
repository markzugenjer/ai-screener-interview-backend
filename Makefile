.PHONY: up tidy dev worker test agent-dev agent-test admin

tidy: ## download dependencies and create go.sum (run once after cloning)
	go mod tidy

up: ## run API, worker and voice agent in one terminal (AGENT=0 skips the agent)
	AGENT=$(or $(AGENT),1) ./scripts/dev.sh

dev: ## run the API (reads .env)
	set -a && . ./.env && set +a && go run ./cmd/api

worker: ## run the scoring worker
	set -a && . ./.env && set +a && go run ./cmd/worker

test: ## run Go tests with the race detector
	go test -race ./...

agent-dev: ## run the voice agent in development mode
	cd agent && pnpm install && pnpm download-files && pnpm dev

agent-test:
	cd agent && pnpm test && pnpm typecheck

admin: ## create a recruiter: make admin EMAIL=a@b.com NAME="Rina"
	set -a && . ./.env && set +a && go run ./cmd/admin create -role recruiter -email "$(EMAIL)" -name "$(NAME)"
