# container-image

A function built from a [Dockerfile](./Dockerfile), on a function url of its own, in [koldstart.pkl](./koldstart.pkl): `status` answers with the version it was built as and the region it runs in.

- `image { ... }` builds the `Dockerfile` in the project's directory, the defaults, with docker for the function's architecture, `arm64`, and pushes it to the ecr repository `koldstart/status`. A function with an image has no `runtime` or `handler`.
- `buildArgs` passes `VERSION` to the build, which stamps it into the binary.
- the image holds the go binary alone, `FROM scratch`: aws-lambda-go talks to lambda's runtime api itself. [go.mod](./go.mod) pins `toolchain go1.27.3`, the go of the build stage.
- the image is tagged by a hash of what it is built from, and [.dockerignore](./.dockerignore) leaves out what it does not need, so a change to this README builds nothing.
- `url = new { auth = "none" }` gives it a public url, the output `statusUrl`.

## deploying it

It needs docker running.

```bash
kdst deploy
curl "$(kdst outputs -o json | jq -r .statusUrl)"
```

`curl` prints `{"region":"eu-west-1","version":"1.0.0"}`. Change `VERSION` and deploy again, and a new image is built and pushed; deploy with nothing changed, and none is. `kdst remove` deletes the stage.
