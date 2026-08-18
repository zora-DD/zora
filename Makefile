.PHONY: run run-postgres build-mcp-files build-mcp-microsoft build-mcp-connectors postgres-up postgres-down test-postgres eval-rag eval-memory eval-agents test fmt vet check

run:
	@set -a; \
	if [ -f .env.local ]; then . ./.env.local; elif [ -f .env ]; then . ./.env; fi; \
	set +a; \
	go run ./cmd/zora

run-postgres:
	@set -a; \
	if [ -f .env.local ]; then . ./.env.local; elif [ -f .env ]; then . ./.env; fi; \
	set +a; \
	ZORA_STORE_PROVIDER=postgres \
	ZORA_POSTGRES_DSN='postgres://zora:zora@localhost:54328/zora?sslmode=disable' \
	go run ./cmd/zora

build-mcp-files:
	mkdir -p ./bin
	go build -o ./bin/zora-mcp-files ./cmd/zora-mcp-files

build-mcp-microsoft:
	mkdir -p ./bin
	go build -o ./bin/zora-mcp-microsoft ./cmd/zora-mcp-microsoft

build-mcp-connectors: build-mcp-files build-mcp-microsoft

postgres-up:
	docker compose up -d postgres

postgres-down:
	docker compose down

test-postgres:
	ZORA_TEST_POSTGRES_DSN='postgres://zora:zora@localhost:54328/zora?sslmode=disable' \
	go test ./internal/store/postgres -run TestPostgresConversationAndKnowledgeLifecycle -count=1

eval-rag:
	go run ./cmd/zora-eval -dataset ./evals/knowledge.json

eval-memory:
	go run ./cmd/zora-memory-eval -dataset ./evals/memory.json

eval-agents:
	go run ./cmd/zora-agent-eval -dataset ./evals/agents.json

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

check: fmt test vet
