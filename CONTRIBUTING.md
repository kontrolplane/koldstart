# contributing to koldstart

by participating you agree to the [code of conduct](CODE_OF_CONDUCT.md).

## setup

needs go 1.26+ and make. the tools `make` runs, golangci-lint and the rest, are pinned in the Makefile and built by `go run` the first time. the schema tests need [pkl](https://pkl-lang.org/); without it they skip. the tests that talk to aws need docker, for [floci](https://github.com/floci-io/floci).

```sh
git clone https://github.com/kontrolplane/koldstart.git
cd koldstart
make test
```

- `make build` builds `bin/koldstart` and `bin/kdst`.
- `make emulator test-emulator` runs the tests that talk to aws against floci; `make emulator-down` stops it.
- `make check` runs what ci runs.

[AGENTS.md](AGENTS.md) has the layout and the rules. [#1](https://github.com/kontrolplane/koldstart/issues/1) has the design and the milestones.

## pull requests

- open an issue first for anything bigger than a fix.
- one change per pull request, titled `feat:`, `fix:`, `docs:` or `chore:`.
- run `make check` before pushing.
- a change to what koldstart creates in aws comes with a test against the fake providers.

## bugs and features

use the [issue templates](https://github.com/kontrolplane/koldstart/issues/new/choose). security issues go through [SECURITY.md](SECURITY.md).

## license

contributions are licensed under [Apache-2.0](LICENSE).
