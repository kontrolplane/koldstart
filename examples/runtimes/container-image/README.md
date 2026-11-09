# container-image

A status endpoint in go, built from a [Dockerfile](./Dockerfile) into a container image and run by lambda as it is, on a public function url: `status` answers with the version and commit it was built from, the region it runs in, the version of the function, and whether the request was its instance's cold start.

## how koldstart builds it

- `image { ... }` in [koldstart.pkl](./koldstart.pkl) makes `status` a container image function. It has no `runtime` or `handler`: koldstart infers neither for a function with an image, and the image's `ENTRYPOINT` is what lambda runs. `command`, `entryPoint` and `workingDirectory` replace the image's own, and `dockerfile` and `context` name another Dockerfile and directory than the defaults, `Dockerfile` and `.`.
- koldstart runs `docker buildx build --platform linux/arm64 --provenance=false --sbom=false`, for the function's `architecture`, with each of `buildArgs` as a `--build-arg`, and pushes the image to the ecr repository `koldstart/status`, which it makes for the app. Lambda refuses the attestations buildx adds by default, so koldstart turns them off.
- the image is tagged `<stage>-status-<hash>`, by a hash of what it is built from: the Dockerfile, the build arguments, the platform, and every file of the context that [.dockerignore](./.dockerignore) leaves in. The same build is pushed once, and a deploy that changes none of it builds nothing and leaves the function as it is. The `.dockerignore` leaves out this README, the pkl, `.koldstart` and the tests, so a change to them builds no new image.
- `VERSION` and `REVISION` are read from the environment koldstart runs in, `read?("env:VERSION")`, so continuous integration stamps the image with the tag and the commit it builds, and a new version or commit builds a new image. A build on a laptop, without them, is `0.0.0-development` and `unknown`.
- the [Dockerfile](./Dockerfile) builds the go binary in a stage pinned by digest, with the go [go.mod](./go.mod) pins as its `toolchain`, `go1.27.3`, and copies it alone into `FROM scratch`: aws-lambda-go talks to the lambda runtime api itself, so the image needs nothing else. Its oci labels name its source, version, revision and licence.

The url is public, `auth = "none"`, because a status endpoint is read by browsers and uptime monitors that sign nothing, and it answers with nothing but what was deployed; cors lets `https://status.example.com` read it. It takes `GET` and `HEAD` of `/`, and answers anything else with a `405` or `404` in `application/problem+json`. Every answer has `cache-control: no-store`, `strict-transport-security`, `x-content-type-options: nosniff`, and the request's id as `x-request-id`. The function logs one json line for each request, at the level lambda sets as `AWS_LAMBDA_LOG_LEVEL`.

## trying it

It needs docker running.

```bash
VERSION=1.4.0 REVISION=$(git rev-parse --short HEAD) kdst deploy --stage development
curl --include "$(kdst outputs --stage development --output json | jq --raw-output .statusUrl)"
```

`curl` prints `{"version":"1.4.0","revision":"<commit>","region":"eu-west-1","functionVersion":"$LATEST","coldStart":true}`, and `coldStart` is `false` from the second request an instance answers. `statusUrl` is the output every function url gets, `<function>Url`.

```bash
kdst invoke --stage development --function status --local --path .koldstart/events/status/get-status.json
kdst invoke --stage development --function status --local --path .koldstart/events/status/post-status.json
```

`--local` builds the image into docker on this machine, for its own architecture, and runs it against koldstart's emulator of the lambda runtime api: the first answers with the status, the second with the `405`. `kdst remove --stage development` deletes the stage.

## testing it

```bash
go test -race -count=1 ./...
```

The tests run the handler without lambda: what it answers each method and path with, the headers of every answer, and the cold start.

## related

- [function-urls](../../concepts/function-urls): a function's own url, its `auth` and its cors
- [outputs](../../concepts/outputs): `statusUrl` and the other outputs a stage gets without asking
- [defaults](../../concepts/defaults): json logs and tracing for every function of a project
- [alarms](../../concepts/alarms): the errors, throttles and duration of a function, paging on production
