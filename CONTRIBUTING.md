# contributing to koldstart

by participating you agree to the [code of conduct](CODE_OF_CONDUCT.md).

## setup

needs go 1.26+ and make. `make lint` installs the golangci-lint it pins into `bin/`.

```sh
git clone https://github.com/kontrolplane/koldstart.git
cd koldstart
make test
make build   # bin/koldstart and its bin/kdst link
make check   # what CI runs
```

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
