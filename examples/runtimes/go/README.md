# go

Feature flags in go, on two routes of the stage's http api: `GET /flags/{name}` answers with a flag and its version as its `etag`, and `PUT /flags/{name}` makes it, or changes it when `if-match` names the version it is at, so two changes made to one version cannot both be stored. The flags are kept in a dynamodb table.

## how koldstart builds it

- `api` in [koldstart.pkl](./koldstart.pkl) sets no `runtime` or `handler`. [cmd/api](./cmd/api) holds go files, so koldstart infers `runtime = "provided.al2023"` and `handler = "./cmd/api"`, the package to build. Setting both does the same for a package elsewhere, such as `handler = "./services/flags"`; a directory named after the function under `cmd/` is the convention.
- koldstart cross-compiles the package from the directory of `koldstart.pkl`, where [go.mod](./go.mod) is, with `CGO_ENABLED=0` and `GOOS=linux GOARCH=arm64` for the function's `architecture`:

  ```bash
  go build -trimpath -buildvcs=false -tags lambda.norpc -ldflags "-s -w -buildid=" -o bootstrap ./cmd/api
  ```

  `lambda.norpc` leaves out the rpc mode of aws-lambda-go, which `provided.al2023` does not use. The binary goes in the zip as `bootstrap`, which the runtime runs.
- the flags keep paths of this machine, the commit and the build id out of the binary, and go.mod pins `toolchain go1.27.3`, so the same code builds the same bytes on a laptop and in continuous integration, and the zip's hash says whether the code changed. A deploy whose only change is code uploads the zip and makes one `UpdateFunctionCode` call.
- dependencies are compiled in, from the versions go.sum records. A function needs go on the `PATH` to build, and no docker.

The handler is split from its wiring: [main.go](./cmd/api/main.go) reads `FLAGS_NAME`, which `link` sets, and starts lambda; [handler.go](./cmd/api/handler.go) answers the routes; [store.go](./cmd/api/store.go) keeps the flags in the table behind a small interface, with a conditional put that refuses a change to a version the flag is no longer at. A refused change is a `412`, or a `428` for a `PUT` without `if-match` of a flag that exists, and every error is `application/problem+json`. The function logs one json line for each request, at the level lambda sets as `AWS_LAMBDA_LOG_LEVEL`, with `message` as the key of what happened.

## trying it

```bash
kdst deploy --stage development
URL="$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl --request PUT "$URL/flags/new-checkout" --data '{"enabled": false, "description": "the new checkout page"}'
curl --include "$URL/flags/new-checkout"
curl --request PUT "$URL/flags/new-checkout" --header 'if-match: "1"' --data '{"enabled": true, "description": "the new checkout page"}'
```

The first answers `201` with version 1, the second with the flag and `etag: "1"`, and the third `200` with version 2. Sending the third again answers `412`: the flag is at version 2 now.

```bash
kdst invoke --stage development --function api --local --path .koldstart/events/api/get-flag.json
```

`--local` builds the package for this machine and runs it against koldstart's emulator of the lambda runtime api, with the variables the stage deployed and your credentials, so it reads the table the stage deployed. [.koldstart/events/api](./.koldstart/events/api) has an event for each case. `kdst remove --stage development` deletes the stage, and leaves the table in place, as it holds data.

## testing it

```bash
go test -race -count=1 ./...
```

The tests run the handler against a fake of the table, with no aws account: each route, a name that cannot be one, a body without `enabled`, a change to the version a flag is at and to one it is no longer at, the etag of every flag, and a failure that answers with the request's id and nothing of its cause.

## related

- [http-routes](../../concepts/http-routes): the stage's http api, its routes, limits and access logs
- [link](../../concepts/link): what `["flags"] = "readwrite"` grants, and the variables it sets
- [resources](../../concepts/resources): `kdst.table()` and the other shorthands
- [outputs](../../concepts/outputs): `httpApiUrl`, which every stage with http routes gets
- [defaults](../../concepts/defaults): json logs and tracing for every function of a project
