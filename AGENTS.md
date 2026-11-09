# koldstart: notes for contributors and coding agents

## layout

```
cmd/koldstart/              the binary; main calls internal/cli. kdst is a link to it
pkl/                        the schema package: Project.pkl, the shape of koldstart.pkl, kdst.pkl, its helpers and events, and PklProject; schema.go embeds it and writes the koldstart dependency of a project
internal/cli/               the cobra commands, the global flags and the aws session; Console, what kdst ui works with
internal/tui/               kdst ui, with styles/ for relay's palette and keys/ for the key map
internal/pkl/               finds or downloads pkl, and evaluates configuration to json in its pkl project, resolving it first
internal/config/            decodes what pkl renders, and checks what the schema cannot
internal/build/             node bundled with esbuild, go cross-compiled, rust built with cargo lambda, a bootstrap built elsewhere checked, python's dependencies installed by uv, container images built; deterministic zips
internal/desired/           turns a configuration into nodes: names, policies, link, events, resources
internal/engine/            plans and applies nodes in dependency order, recording every step in the state
internal/state/             the state, locks and zips in a bucket of the account; a memory backend for tests
internal/provider/awsp/     the hand-written providers, with koldstart:: type names
internal/provider/cloudcontrol/  everything in resources, through cloud control
internal/resolve/           reads secrets from ssm and secrets manager, and outputs of other stages
internal/local/             runs functions here against an emulator of the lambda runtime api; offline
internal/migrate/           converts a serverless.yml into a koldstart.pkl
internal/scaffold/          kdst init's templates: a koldstart.pkl, a handler in typescript, go, python or rust, the .gitignore entries, and the PklProject beside them
internal/awserr/            tells aws errors apart by their code, the one way every package does
internal/awstest/           connects tests to the emulator
hack/schema/                packages the schema in pkl/ with the pkl koldstart pins, for releases: the metadata at koldstart@<v> and the zip
hack/pkl/                   downloads the pinned pkl for a platform, for the image
hack/floci/                 the local aws profile for floci, for running kdst against it by hand
hack/demonstration/         the checkout service make local and make vhs deploy to floci, the script that seeds it, and backfill, which writes what floci does not keep
examples/                   concepts/, one thing koldstart does each in a file that fits on a screen; runtimes/, how each language is built, with its tests; reference-application/, the two together in one application, which the e2e tests deploy. make test-examples runs their tests, a go module of its own is tidied and linted by make lint, and examples_test.go checks every one evaluates
test/e2e/                   deploys examples/reference-application to a real account, behind the e2e build tag
vhs/                        make vhs: the tape that records kdst ui for the README, its gif and a screenshot of each page, and the floci and profile it records against
assets/                     the README's header, footer and cassette.gif, and the screenshots make vhs takes in pages/
internal/version/           the build's version, stamped by the linker: version, commit, date, go and platform
docs/                       the user documentation, which the README links to
```

[#1](https://github.com/kontrolplane/koldstart/issues/1) has the design and the milestones koldstart was built in, all of them done. There is no release yet.

## rules

- no cgo. the binary is static, `CGO_ENABLED=0`, for linux and darwin on amd64 and arm64. no windows in v1.
- the binary answers to `koldstart` and `kdst`. help, examples and errors use `cli.Name`, never a hardcoded name.
- a global flag reads its default from the environment variable the README lists, and the flag wins over it.
- no stub commands: a command lands with what it does.
- koldstart sends nothing anywhere but the aws apis of the account it deploys to, and the downloads of pkl and the schema package. no telemetry.
- `plan` changes nothing in aws and writes no state.
- tests: standard library only, no testify. table tests with `tests := []struct{...}`, a doc comment on each test.
- tests come in three tiers:
  - unit: `make test`. aws calls go behind small interfaces, so the engine, providers and tui are tested against fakes. no network, no docker.
  - emulator: `make emulator test-emulator`, then `make emulator-down`, against Floci (`docker-compose.yaml`), which runs lambda in the real runtime images and so mounts the docker socket. a test that talks to aws has `Emulator` in its name and gets its config from `awstest.Emulator(t)`, which skips without `KOLDSTART_TEST_ENDPOINT`. Floci ignores `ExpectedBucketOwner`, has no `DescribeType`, its cloud control has no `UpdateResource`, and an http api answers on 127.0.0.1:4566 with the api's host name in the `Host` header (`awstest.Get`). it has no `DeleteRouteSettings`, it deletes a rest api's deployment a stage still serves, which aws refuses, and a load balancer's listener cannot be reached, only its api. test what it does not do against fakes that behave as aws does.
  - e2e: `go test -tags e2e ./test/e2e/...`, against a real sandbox account, for what no emulator gets right. each run deploys its own stage, `e2e-<run>`, with an amending `e2e.pkl` that deletes what holds data, and removes it; the `E2E` workflow runs it once `KOLDSTART_E2E_ROLE_ARN` is set. `make lint` lints it with the tag.
- a `koldstart.pkl` imports the schema as `@koldstart/Project.pkl` and `@koldstart/kdst.pkl`, from the koldstart dependency of the `PklProject` beside it or above it. `PklProject.deps.json` is its lock file and is committed, never ignored. a release writes its own version's package uri; a dev build, which has no release, writes a local dependency on the schema it embeds, extracted into the cache under a hash of its files. `schema.Current` is the one place that chooses.
- an example's `PklProject` names the package of the first release, and the example is evaluated against `pkl/` in this tree with `schema.Local`, as `migrate`'s check is, so it needs neither the package nor its lock file, which cannot exist before that release is published. testdata depends on `pkl/` as a local dependency, `import("…/pkl/PklProject")`, and commits its lock file. an example's go module pins its `toolchain`, so its zips are the same everywhere.
- `schemaVersion` in `pkl/Project.pkl` and `config.SchemaVersion` go up together whenever what Project.pkl renders changes in a way an older koldstart cannot read: a new field it must act on, or a changed shape. adding something an older one may safely ignore does not count. nothing was released before 0.1.0, so it is 1 there.
- `internal/migrate` is tested with golden files: a `serverless.yml` under `testdata/<case>/`, and the `koldstart.pkl` and `todos.txt` it converts to. after changing the conversion, run `go test ./internal/migrate -update`, read the diff, and commit it. every golden file must evaluate with pkl and pass a deploy's checks.
- logs: `log/slog`. `--debug` writes them to `debug.log`, otherwise nothing is logged.
- errors wrap with what was being done (`fmt.Errorf("reading the state of %s: %w", stage, err)`). an error a person reads says what to do.
- `make check` runs most of what ci runs: golangci-lint, actionlint, `go mod tidy`, govulncheck, the unit tests and the goreleaser config. ci also runs the emulator tests, the examples' tests (`make test-examples`), a snapshot release (`make snapshot`) and, on a pull request, the image build. the Makefile is plain targets with a comment above each; keep it that way.
- base images and actions are pinned by digest or commit, with the tag beside it.
- writing: short and concrete. say what something does and the command or value, not why at length. comments only explain a non-obvious why. british spelling.
- comments go above what they describe: a function, type, constant, variable or field. none inside function bodies and none at the end of a line. a `//nolint` goes in the doc comment of the function it covers, after a `//` line. the one exception: the version beside a pinned action, `@<sha> # v7.0.1`, which dependabot reads.
- case: README headings lowercase, prose sentence case. SECURITY, CONTRIBUTING, AGENTS and design docs all lowercase.
- commits: conventional, lowercase, imperative (`feat: read the stage from KOLDSTART_STAGE`).

## the terminal interface

`kdst ui` follows relay (kontrolplane/relay), the newest of the kontrolplane tuis. read its `pkg/tui` before adding a view.

- it lives in `internal/tui`, with `internal/tui/styles` and `internal/tui/keys`. cobra never imports it: `cli.Root` takes `tui.Run`, which runs it with `tea.WithContext`. everything it reads and changes goes through `cli.Console`, behind the tui's `backend` interface.
- charm.land v2: bubbletea, bubbles and lipgloss.
- one model with a `page` enum and a state struct per page, each with its switch, update, view and go back. a confirmation is a page drawn over the one it came from.
- keys are `key.Binding`s in one key map. `ctrl+c` quits, `q` goes back and quits only on the app overview, `esc` goes back or cancels and never quits. letters are off while a text input has focus.
- styles are relay's palette, `--theme auto|dark|light` and `styles.Clean`. every string from aws, and every error, goes through `Clean` before it reaches the terminal.
- every load is a `tea.Cmd` with a timeout, and its message carries the generation it was asked for; a stale one is dropped. `Init` cannot change the model, so `newModel` numbers the first load. refresh is a tick that pause lets lapse.
- a deploy or remove runs in a goroutine that sends its progress on a channel, and quitting stops it and waits for it, so its lock is released.
- tests drive `Update` and `View` directly against a fake backend, with relay's layout sweep over terminal sizes and its escape test. no teatest.
- a frame draws only the lines in sight (`lazyLines`), so a page of thousands of lines costs what a screenful does; what a page keeps drawn is keyed by width, read and `styles.Gen`. `go test -run - -bench . ./internal/tui` draws each long page of a big account (`perf_test.go`) at 200x60, each well under 16ms. the fuzz targets (`FuzzRenderCell`, `FuzzTint`, `FuzzJSONLines`, `FuzzDiff`, `FuzzInvocationLine` and `FuzzJSONView` in `internal/tui`, `FuzzClean` in styles; run one with `go test -fuzz FuzzTint ./internal/tui`) check what is drawn is clean and fits its width; commit what they find under `testdata/fuzz`.
- json on a page is a `jsonView` (`jsonview.go`), as on the resource and output pages, the inputs of a create in a plan, the page of a log line and the invoke tab's event and result: kept in the page's state, given the keys with `update` before the page's own, drawn with `lines` or `view`, the page's scroll following its cursor with `followJSON`, and returned by `pages.json` so the frame shows its path and search and the search takes what is typed. `jsonLineCell` tints one line of indented json as it does. `BenchmarkJSONView` draws one of 2 mb and one of 10,000 lines, and `FuzzJSONView` draws any bytes after any keys.
- the header shows the profile, region and account, from sts `GetCallerIdentity`, and the state bucket.
- alignment follows one grid, as relay's panels do:
  - a page's structure starts one column in: its title, its line of facts, its tab bar, its section headers and a table's header row's rule.
  - its rows start three columns in, past the cursor's column: a table's rows, key/value rows, json, the lines of a log, a plan's changes and a diff. `rowInset` and `insetLines` draw them; `rowIn` draws a key/value row.
  - a key/value row's label is right aligned, `labelWidth` wide with a gap of two, so values start in one column on every page and in both columns of a two-column page. A note in place of rows starts where the values do.
  - numbers are right aligned. What a number is when it is not a count, as p99 or max, sits faint in a column of its own before it, and counts that sit side by side, as `+3 ~1 -0`, are padded to the widest of their column.
  - a sparkline spans its whole window, its quiet minutes dotted, so it always ends at now.
