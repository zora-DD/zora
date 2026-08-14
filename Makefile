.PHONY: run test fmt vet check

run:
	go run ./cmd/zora

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

check: fmt test vet

