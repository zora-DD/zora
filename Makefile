.PHONY: run run-postgres postgres-up postgres-down test-postgres eval-rag test fmt vet check

run:
	go run ./cmd/zora

run-postgres:
	ZORA_STORE_PROVIDER=postgres \
	ZORA_POSTGRES_DSN='postgres://zora:zora@localhost:54328/zora?sslmode=disable' \
	go run ./cmd/zora

postgres-up:
	docker compose up -d postgres

postgres-down:
	docker compose down

test-postgres:
	ZORA_TEST_POSTGRES_DSN='postgres://zora:zora@localhost:54328/zora?sslmode=disable' \
	go test ./internal/store/postgres -run TestPostgresConversationAndKnowledgeLifecycle -count=1

eval-rag:
	go run ./cmd/zora-eval -dataset ./evals/knowledge.json

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

check: fmt test vet
