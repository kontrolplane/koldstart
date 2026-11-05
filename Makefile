# The tasks of developing koldstart. `make check` runs what ci runs.

version := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
commit  := $(shell git rev-parse HEAD 2>/dev/null || echo none)
date    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
stamp   := github.com/kontrolplane/koldstart/internal/version
ldflags := -s -w -X $(stamp).buildVersion=$(version) -X $(stamp).buildCommit=$(commit) -X $(stamp).buildDate=$(date)

# The tools, at the versions ci uses. go run builds each once and caches it.
golangci-lint := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
govulncheck   := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
actionlint    := go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
goreleaser    := go run github.com/goreleaser/goreleaser/v2@v2.20.0

.PHONY: check test lint vuln build image snapshot schema emulator emulator-down test-emulator

# Everything ci checks.
check: lint vuln test
	$(goreleaser) check

# The unit tests.
test:
	go test -race -count=1 ./...

# The code, the workflows, and whether go.mod is tidy.
lint:
	go mod tidy -diff
	$(golangci-lint) run
	$(actionlint)

# Known vulnerabilities in what koldstart calls.
vuln:
	$(govulncheck) ./...

# bin/koldstart, and bin/kdst beside it.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(ldflags)" -o bin/koldstart ./cmd/koldstart
	ln -sf koldstart bin/kdst

# The container image, as koldstart:dev.
image:
	docker build --build-arg VERSION=$(version) --build-arg COMMIT=$(commit) --build-arg DATE=$(date) -t koldstart:dev .

# The release archives, the cask and the schema package in dist/, without publishing them.
snapshot:
	$(goreleaser) release --snapshot --clean

# The pkl schema package in .pkl-out/, as a release attaches it, as version 0.0.0-dev.
schema:
	go run ./hack/schema -version 0.0.0-dev -out .pkl-out

# Floci, the aws emulator, on 127.0.0.1:4566.
emulator:
	docker compose up --detach --wait

# Stops floci, and drops what it held.
emulator-down:
	docker compose down

# The tests that talk to aws, against floci. Start it with make emulator first.
test-emulator:
	KOLDSTART_TEST_ENDPOINT=http://127.0.0.1:4566 go test -race -count=1 -run Emulator ./...
