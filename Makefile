.PHONY: run eval-rag test fmt vet check

run:
	go run ./cmd/zora

eval-rag:
	go run ./cmd/zora-eval -dataset ./evals/knowledge.json

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

check: fmt test vet
