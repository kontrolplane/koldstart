# contributing to koldstart

by participating you agree to the [code of conduct](CODE_OF_CONDUCT.md).

## setup

needs go 1.26+ and make. the tools `make` runs, golangci-lint and the rest, are pinned in the Makefile and built by `go run` the first time. the tests that evaluate pkl need [pkl](https://pkl-lang.org/), and those that run functions here need node and python3; without them they skip. the tests that talk to aws need docker, for [floci](https://github.com/floci-io/floci).

```sh
git clone https://github.com/kontrolplane/koldstart.git
cd koldstart
make test
```

- `make build` builds `bin/koldstart` and `bin/kdst`.
- `make emulator test-emulator` runs the tests that talk to aws against floci; `make emulator-down` stops it.
- `make local` starts floci and deploys the checkout service in [hack/demonstration/checkout](hack/demonstration/checkout) to it, to development, staging and production, invoked and with an hour of metrics; `make local-ui` then opens `kdst ui` on it.
- [hack/floci/aws-config](hack/floci/aws-config) is an aws profile for floci, to run kdst and the aws cli against it by hand: `AWS_CONFIG_FILE=hack/floci/aws-config kdst --profile local deploy`, or copy the profile into `~/.aws/config`.
- `make vhs` records the gif in the README and a screenshot of each page in [assets/pages](assets/pages), against a floci of its own on 127.0.0.1:4567. it needs vhs, ffmpeg, ttyd and gifsicle.
- `make check` runs what ci runs but the emulator tests, the snapshot release (`make snapshot`) and the image (`make image`).

## e2e tests

`go test -tags e2e ./test/e2e/...` deploys [examples/full-featured](examples/full-featured) to a stage of its own, `e2e-<run>`, in the account of the credentials you have, works with it, and removes it, deleting the table and queue too. use a sandbox account: it creates real resources, for a few minutes. it needs node and the aws cli besides go.

in ci, the `E2E` workflow runs on every push to main and every night, once the repository variable `KOLDSTART_E2E_ROLE_ARN` names a role in the sandbox account that github's oidc provider may assume for this repository, with permission to manage what the example deploys and the state bucket. `KOLDSTART_E2E_REGION` picks the region, `eu-west-1` without it. each run first removes the stages earlier runs left behind for more than six hours.

[AGENTS.md](AGENTS.md) has the layout and the rules. [#1](https://github.com/kontrolplane/koldstart/issues/1) has the design and the milestones koldstart was built in, all of them done.

## pull requests

- open an issue first for anything bigger than a fix.
- one change per pull request, titled `feat:`, `fix:`, `perf:`, `test:`, `docs:` or `chore:`.
- run `make check` before pushing.
- a change to what koldstart creates in aws comes with a test against the fake providers.

## bugs and features

use the [issue templates](https://github.com/kontrolplane/koldstart/issues/new/choose). security issues go through [SECURITY.md](SECURITY.md).

## license

contributions are licensed under [Apache-2.0](LICENSE).
