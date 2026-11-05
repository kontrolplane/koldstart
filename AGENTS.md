# koldstart: notes for contributors and coding agents

## layout

```
cmd/koldstart/              the binary; main calls internal/cli. kdst is a link to it
pkl/                        the schema package: Project.pkl, the shape of koldstart.pkl, and kdst.pkl, its helpers and events
internal/cli/               the cobra commands, the global flags and the aws session
internal/pkl/               finds or downloads pkl, and evaluates configuration to json
internal/config/            decodes what pkl renders, and checks what the schema cannot
internal/build/             node bundled with esbuild, go cross-compiled; deterministic zips
internal/desired/           turns a configuration into nodes: names, policies, link, events, resources
internal/engine/            plans and applies nodes in dependency order, recording every step in the state
internal/state/             the state, locks and zips in a bucket of the account; a memory backend for tests
internal/provider/awsp/     the hand-written providers, with koldstart:: type names
internal/provider/cloudcontrol/  everything in resources, through cloud control
internal/awstest/           connects tests to the emulator
internal/version/           the build's version, stamped by the linker, in route's shape
docs/                       the user documentation; it describes the design in #1 until it is built
```

The packages still to come, and the milestones they land in, are in [#1](https://github.com/kontrolplane/koldstart/issues/1).

## rules

- no cgo. the binary is static, `CGO_ENABLED=0`, for linux and darwin on amd64 and arm64. no windows in v1.
- the binary answers to `koldstart` and `kdst`. help, examples and errors use `cli.Name`, never a hardcoded name.
- a global flag reads its default from the environment variable the README lists, and the flag wins over it.
- commands come with their milestone. no stub commands for what is not built yet.
- koldstart sends nothing anywhere but the aws apis of the account it deploys to, and the downloads of pkl and the schema package. no telemetry.
- `plan` changes nothing in aws and writes no state.
- tests: standard library only, no testify. table tests with `tests := []struct{...}`, a doc comment on each test.
- tests come in three tiers:
  - unit: `make test`. aws calls go behind small interfaces, so the engine, providers and tui are tested against fakes. no network, no docker.
  - emulator: `make emulator test-emulator`, against Floci (`docker-compose.yaml`), which runs lambda in the real runtime images and so mounts the docker socket. a test that talks to aws has `Emulator` in its name and gets its config from `awstest.Emulator(t)`, which skips without `KOLDSTART_TEST_ENDPOINT`. Floci ignores `ExpectedBucketOwner`, has no `DescribeType`, its cloud control has no `UpdateResource`, and an http api answers on 127.0.0.1:4566 with the api's host name in the `Host` header (`awstest.Get`). test what it does not do against fakes that behave as aws does.
  - e2e: `go test -tags e2e ./test/e2e/...`, against a real sandbox account, for what no emulator gets right.
- logs: `log/slog`. `--debug` writes them to `debug.log`, otherwise nothing is logged.
- errors wrap with what was being done (`fmt.Errorf("reading the state of %s: %w", stage, err)`). an error a person reads says what to do.
- `make check` runs what CI runs: lint for linux and darwin, actionlint, govulncheck, the tests and the goreleaser config.
- base images and actions are pinned by digest or commit, with the tag beside it.
- writing: short and concrete. say what something does and the command or value, not why at length. comments only explain a non-obvious why. british spelling.
- comments go above what they describe: a function, type, constant, variable or field. none inside function bodies and none at the end of a line. a `//nolint` goes in the doc comment of the function it covers, after a `//` line. the one exception: the version beside a pinned action, `@<sha> # v7.0.1`, which dependabot reads.
- case: README headings lowercase, prose sentence case. SECURITY, CONTRIBUTING, AGENTS and design docs all lowercase.
- commits: conventional, lowercase, imperative (`feat: read the stage from KOLDSTART_STAGE`).

## the terminal interface

`kdst ui` follows relay (kontrolplane/relay), the newest of the kontrolplane tuis. read its `pkg/tui` before adding a view.

- it lives in `internal/tui`, with `internal/tui/styles` and `internal/tui/keys`. cobra never imports it: `cli.Root` takes the tui's run function, as konverge does, and runs it with `tea.WithContext`.
- charm.land v2: bubbletea, bubbles, lipgloss and huh.
- one model with a `page` enum and a state struct per page, each with its switch, update, view and go back. a confirmation is a page drawn over the one it came from.
- keys are `key.Binding`s in one key map. `ctrl+c` quits, `q` goes back and quits only on the app overview, `esc` goes back or cancels and never quits. letters are off while a text input has focus.
- styles are relay's palette, `--theme auto|dark|light` and `styles.Clean`. every string from aws, and every error, goes through `Clean` before it reaches the terminal.
- every load is a `tea.Cmd` with a timeout, and its message carries the generation it was asked for; a stale one is dropped. refresh is a tick that pause lets lapse.
- tests drive `Update` and `View` directly, with relay's layout sweep over terminal sizes and its escape test. no teatest.
- the header shows the profile, region and account, from sts `GetCallerIdentity`, and the state bucket.
