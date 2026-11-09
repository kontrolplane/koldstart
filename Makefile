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

.PHONY: check test test-examples lint vuln build image snapshot schema emulator emulator-down test-emulator local local-ui vhs

# Everything ci checks.
check: lint vuln test
	$(goreleaser) check

# The unit tests.
test:
	go test -race -count=1 ./...

# The examples' own tests: the unit tests of the reference application and of each runtime, in go,
# typescript, python, rust and shell, and the typescript's types. The node packages are installed
# first, and python's dependencies into a virtual environment of the runtime's own.
test-examples:
	cd examples/reference-application && go test -race -count=1 ./... && npm ci --no-audit --no-fund && npm test && npm run typecheck
	cd examples/runtimes/typescript && npm ci --no-audit --no-fund && npm test && npm run typecheck
	cd examples/runtimes/go && go test -race -count=1 ./...
	cd examples/runtimes/container-image && go test -race -count=1 ./...
	cd examples/runtimes/python && python3 -m venv .venv && .venv/bin/python -m pip install --quiet --disable-pip-version-check --require-hashes --requirement requirements.txt && .venv/bin/python -B -m unittest discover --start-directory tests --top-level-directory .
	cd examples/runtimes/rust/cmd/quote && cargo test --locked && cargo clippy --locked --all-targets -- --deny warnings
	cd examples/runtimes/bootstrap && python3 -B -m unittest discover --start-directory tests

# The code, the examples' go, the e2e tests behind their build tag, the workflows, and whether
# the go.mod files are tidy.
lint:
	go mod tidy -diff
	cd examples/reference-application && go mod tidy -diff
	cd examples/runtimes/go && go mod tidy -diff
	cd examples/runtimes/container-image && go mod tidy -diff
	$(golangci-lint) run
	$(golangci-lint) run --build-tags e2e ./test/...
	cd examples/reference-application && $(golangci-lint) run ./...
	cd examples/runtimes/go && $(golangci-lint) run ./...
	cd examples/runtimes/container-image && $(golangci-lint) run ./...
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

# Floci with the checkout service in hack/demonstration/checkout deployed to development, staging and
# production over a few releases by its team, invoked, and given an hour of metrics, for kdst ui
# to look at. It takes four minutes, and again after floci restarts, as floci keeps nothing. make
# local-ui opens kdst ui on it.
local: build emulator
	hack/demonstration/seed.sh

# kdst ui on the checkout service make local deployed, on its development stage, through the
# local profile of hack/floci/aws-config, whatever aws settings the shell has.
local-ui: build
	cd hack/demonstration/checkout && env -u AWS_ENDPOINT_URL -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN -u AWS_SHARED_CREDENTIALS_FILE -u AWS_REGION -u AWS_DEFAULT_REGION -u KOLDSTART_CONFIG -u KOLDSTART_STAGE AWS_CONFIG_FILE=../../floci/aws-config ../../../bin/kdst ui --profile local --stage development

# The recording of kdst ui in the README, assets/cassette.gif and the screenshots in
# assets/pages/, made with vhs, ffmpeg and ttyd. It deploys the checkout service in
# hack/demonstration/checkout to a floci of its own on 127.0.0.1:4567, which it starts afresh and removes
# again, so make emulator's is left alone. gifsicle then brings the gif from about 10 mb to 3: the
# README shows it at most 1,000 pixels wide, so 1,800 is sharp on a retina screen.
vhs: build
	docker compose -f vhs/docker-compose.yaml down
	docker compose -f vhs/docker-compose.yaml up --detach --wait
	KOLDSTART_DEMONSTRATION_CONFIG=$(CURDIR)/vhs/aws-config KOLDSTART_DEMONSTRATION_PROFILE=vhs hack/demonstration/seed.sh
	vhs vhs/cassette.tape; status=$$?; docker compose -f vhs/docker-compose.yaml down; exit $$status
	gifsicle --batch -O3 --lossy=80 --colors 64 --resize-width 1800 assets/cassette.gif
