# bootstrap

A clock in sh, in [koldstart.pkl](./koldstart.pkl): `now` answers a `GET` of its function url with the time, and is its own runtime. [bin/now](./bin/now) is a script that asks lambda's runtime api for an invocation, answers it, and asks for the next, as lambda's runtimes do for the languages it knows.

## how koldstart builds it

- `bootstrap = "bin/now"` names an executable built elsewhere, which koldstart zips as it is, as `bootstrap`, the file `provided.al2023` runs. The runtime is inferred from it, `provided.al2023`, and there is no handler.
- koldstart checks it before it zips it: it must be there, executable, and either a script that starts with `#!` or a binary for linux and the function's architecture, `arm64`. A binary for this mac, or for `x86_64`, is refused with what to build instead. Any language that compiles to linux goes the same way, as zig, c, swift or dart do: build it first, then deploy.
- the zip holds one file, the bootstrap, with its executable bit, dated 1980, so the same script makes the same zip on any machine.
- the script needs nothing but what `provided.al2023` has: `sh`, `curl`, `grep`, `tr`, `cut` and `date`. It reads the request's id from the runtime api's header, answers on `/response`, and reports an event it cannot handle on `/error`, which lambda counts as a failure of the function.
- each line of its log is json with its level, and lines below `applicationLogLevel`, which lambda sets as `AWS_LAMBDA_LOG_LEVEL`, are left out.

A script is the shortest custom runtime. For anything with more to it than this, a binary in a language that parses json is the better bootstrap; for go and rust, koldstart builds the bootstrap itself, as [go](../go) and [rust](../rust) show.

## trying it

```bash
kdst deploy --stage development
curl --include "$(kdst outputs --stage development --output json | jq --raw-output .nowUrl)"
```

It answers `{"time":"2026-11-09T20:03:34Z","epochSeconds":1794254614}`. A `POST` is answered `405`, in `application/problem+json`, with `allow: GET, HEAD`. `kdst invoke --stage development --function now --path .koldstart/events/now/not-a-request.json` hands it an event that is not a request, which it reports as an error, `NotARequest`.

`kdst invoke --local --stage development --function now --path .koldstart/events/now/get.json` runs the script on this machine against an emulator of the lambda runtime api, with the variables the stage deployed and your aws credentials, so the stage is deployed first. `kdst remove --stage development` deletes the stage.

## testing it

```bash
python3 -B -m unittest discover --start-directory tests
```

The test runs `bin/now` with `sh` against a fake of lambda's runtime api, in python's standard library, and checks what it posts back: the time for a `GET` and a `HEAD`, a `405` for a `POST`, an error for an event that is not a request, one invocation after another on one instance, and the log at the level asked for. It needs `curl` on the `PATH`, and passes with the `sh` of a mac, bash, and of debian, dash.

## related

- [function-urls](../../concepts/function-urls): the url of a function of its own, without an api in front
- [defaults](../../concepts/defaults): json logs and tracing for every function at once
