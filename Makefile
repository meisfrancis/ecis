BINARY := ecis
PKG    := ./cmd/ecis
BIN    := bin/$(BINARY)

# VERSION is stamped into the binary and shown by `ecis -version`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build install test race lint fmt vet tidy clean check run

all: check build

build: ## Compile the binary to ./bin
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)
	@echo "built $(BIN) ($(VERSION))"

install: ## Install into $GOBIN
	go install -ldflags "$(LDFLAGS)" $(PKG)

test: ## Run the test suite
	go test ./...

race: ## Run the test suite under the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the tree
	gofmt -s -w .

lint: ## Fail on unformatted files, then vet
	@unformatted=$$(gofmt -s -l . | grep -v '^$$' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "these files need gofmt -s -w:"; echo "$$unformatted"; exit 1; \
	fi
	go vet ./...

tidy: ## Tidy go.mod
	go mod tidy

check: lint test ## Lint and test

run: build ## Build and run
	$(BIN)

clean:
	rm -rf bin
