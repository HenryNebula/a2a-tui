BIN := a2a-tui

.PHONY: build run test race lint fmt ci smoke clean

build:
	go build -o bin/$(BIN) ./cmd/$(BIN)

run:
	go run ./cmd/$(BIN)

test:
	go test ./...

race:
	go test -race ./...

lint:
	golangci-lint run

fmt:
	gofmt -l -w .

# ci mirrors what GitHub Actions runs.
ci:
	@test -z "$$(gofmt -l .)" || (echo "files need gofmt:" && gofmt -l . && exit 1)
	go vet ./...
	go test -race ./...

# Live-agent smoke test against public A2A agents (needs network; tolerant
# of agent downtime — see docs/live-agents.md). Use ARGS=--strict to fail
# on any non-skipped agent failure.
smoke:
	bash scripts/smoke.sh $(ARGS)

clean:
	rm -rf bin/
