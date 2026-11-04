# `make` lists the targets. `make check` runs what CI runs.

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec
.DEFAULT_GOAL := help

IMG ?= ghcr.io/kontrolplane/koldstart:main
VERSION ?= $(shell { git describe --tags --always --dirty 2>/dev/null || echo dev; } | sed 's/^v//')
COMMIT ?= $(shell git rev-parse --verify -q HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG = github.com/kontrolplane/koldstart/internal/version
LDFLAGS = -s -w \
	-X $(VERSION_PKG).buildVersion=$(VERSION) \
	-X $(VERSION_PKG).buildCommit=$(COMMIT) \
	-X $(VERSION_PKG).buildDate=$(DATE)

# Tools run at pinned versions with go run, which caches the binaries. golangci-lint is installed
# instead, so it can lint for another GOOS than it runs on.
GOLANGCI_LINT_VERSION = v2.14.0
GOLANGCI_LINT = bin/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   = go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
ACTIONLINT    = go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
GORELEASER    = go run github.com/goreleaser/goreleaser/v2@v2.20.0

.PHONY: help fmt lint vuln test test-emulator emulator emulator.down check build image snapshot release.check

##@ development

help: ## list the targets
	@awk 'BEGIN {FS = ":.*##"; printf "\nusage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9.-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

fmt: ## format the code
	go fmt ./...

lint: $(GOLANGCI_LINT) ## lint the code for linux and darwin, and the workflows, and check go.mod is tidy
	go mod tidy -diff
	GOOS=linux $(GOLANGCI_LINT) run
	GOOS=darwin $(GOLANGCI_LINT) run
	$(ACTIONLINT)

$(GOLANGCI_LINT):
	GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv bin/golangci-lint $@

vuln: ## check for known vulnerabilities
	$(GOVULNCHECK) ./...

test: ## run the unit tests
	go test -race -count=1 ./...

# The emulator tests run against MiniStack. Without KOLDSTART_TEST_ENDPOINT they skip.
EMULATOR ?= http://127.0.0.1:4566

emulator: ## start MiniStack on 127.0.0.1:4566 and wait for it
	docker compose up -d
	@for _ in $$(seq 1 60); do curl -fs -o /dev/null $(EMULATOR)/_localstack/health && exit 0; sleep 1; done; echo "ministack did not become ready"; exit 1

emulator.down: ## stop MiniStack and drop what it held
	docker compose down

test-emulator: ## run the tests that talk to aws against MiniStack; make emulator first
	KOLDSTART_TEST_ENDPOINT=$(EMULATOR) go test -race -count=1 -run 'Emulator' ./...

check: lint vuln test release.check ## run what CI runs

##@ build

build: ## build bin/koldstart and its bin/kdst link
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/koldstart ./cmd/koldstart
	ln -sf koldstart bin/kdst

image: ## build the image IMG
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t $(IMG) .

snapshot: ## build the release archives into dist/ without publishing them
	$(GORELEASER) release --snapshot --clean

release.check: ## check .goreleaser.yaml
	$(GORELEASER) check
