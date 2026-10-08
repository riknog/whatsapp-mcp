SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

GO        ?= go
EXE       := $(if $(filter Windows_NT,$(OS)),.exe,)
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
BIN       := bin/whatsapp-mcp$(EXE)
COVER_MIN ?= 80
COVER_OUT := cover.txt

# Dev tools run as `go run <pkg>@<version>`; they are not added to go.mod.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.1
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0
GOSEC       := github.com/securego/gosec/v2/cmd/gosec@v2.29.0

.PHONY: check fmt fmt-check vet staticcheck lint-stdout test build security clean

check: fmt-check vet staticcheck lint-stdout test

fmt:
	gofmt -s -w .

fmt-check:
	@unfmt=$$(gofmt -s -l .); if [ -n "$$unfmt" ]; then echo "gofmt needed:"; echo "$$unfmt"; exit 1; fi

vet:
	$(GO) vet ./...

staticcheck:
	$(GO) run $(STATICCHECK) ./...

# stdout belongs to the MCP transport. The only permitted write is the version line in main.go.
lint-stdout:
	@if grep -rnE 'fmt\.Print|os\.Stdout' --include='*.go' . | grep -v '^\./cmd/whatsapp-mcp/main\.go:'; then echo "stdout write outside the MCP transport (see above)"; exit 1; fi

# Runs tests with -race and fails if any package with code is below COVER_MIN,
# except internal/wa, which is a thin adapter (DoD: >= 50%). On Windows
# internal/config is exempt too: its Unix permission check does not run there
# (the gate still applies on Linux and macOS).
COVER_SKIP := internal/wa
ifeq ($(OS),Windows_NT)
COVER_SKIP := internal/(wa|config)
endif

test:
	$(GO) test -race -count=1 -cover ./... | tee $(COVER_OUT)
	@awk -v min=$(COVER_MIN) -v skip='$(COVER_SKIP)[ \t]' 'BEGIN { bad = 0 } /coverage: [0-9.]+% of statements/ { if ($$0 ~ skip) next; match($$0, /[0-9.]+%/); pct = substr($$0, RSTART, RLENGTH - 1) + 0; if (pct < min) { print "coverage below " min "%: " $$0; bad = 1 } } END { exit bad }' $(COVER_OUT)

build:
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/whatsapp-mcp

security:
	$(GO) run $(GOVULNCHECK) ./...
	$(GO) run $(GOSEC) -quiet ./...
	$(GO) mod verify

clean:
	rm -rf bin $(COVER_OUT) coverage.out
