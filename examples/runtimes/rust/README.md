# rust

Shipping quotes in rust, on `POST /quotes` of the stage's http api: `quote` answers with what a parcel costs to send, from the shop's published rates, by the zone of its destination, its weight and its service, and how many working days it takes.

## how koldstart builds it

- `quote` in [koldstart.pkl](./koldstart.pkl) sets no `runtime` or `handler`. [cmd/quote](./cmd/quote) holds a [Cargo.toml](./cmd/quote/Cargo.toml), so koldstart infers `runtime = "provided.al2023"` and `handler = "./cmd/quote"`, the directory of the crate. Setting both does the same for a crate elsewhere; a directory named after the function under `cmd/` is the convention.
- for lambda, koldstart runs `cargo lambda build --release --arm64` in the crate's directory, `--x86-64` for a function of that `architecture`, and puts the crate's binary in the zip as `bootstrap`, which the runtime runs. A crate of several binaries needs one named after the function or its directory. It needs [cargo-lambda](https://www.cargo-lambda.info/guide/installation.html) on the `PATH`, which cross-compiles with zig, and no docker.
- [Cargo.lock](./cmd/quote/Cargo.lock) is committed and [rust-toolchain.toml](./cmd/quote/rust-toolchain.toml) pins rust 1.99.0, so every machine builds the crate from the same versions with the same compiler. The release profile strips the binary and builds it as one unit with link-time optimisation, for a small binary and a short cold start.
- dependencies are compiled in. `lambda_http` is built with the payload of the http api and tracing alone, `default-features = false`, and turns the event into an `http::Request` and the answer back.

The handler is split from what it answers with: [main.rs](./cmd/quote/src/main.rs) starts the runtime, [handler.rs](./cmd/quote/src/handler.rs) answers the route, and [rates.rs](./cmd/quote/src/rates.rs) makes a quote from a `RateCard`, a trait the published rates and the tests' fake both implement. A parcel that cannot be read is a `400`, one that cannot be sent a `422`, and every error is `application/problem+json`. `lambda_http::tracing::init_default_subscriber()` logs at the level lambda sets as `AWS_LAMBDA_LOG_LEVEL`, and as json lines when lambda sets `AWS_LAMBDA_LOG_FORMAT` to json, as `logFormat = "json"` makes it.

## trying it

```bash
kdst deploy --stage development
URL="$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl --request POST "$URL/quotes" --data '{"destination": "DE", "weightGrams": 1200, "service": "express"}'
curl --request POST "$URL/quotes" --data '{"destination": "JP", "weightGrams": 2500, "service": "express"}'
```

The first answers `{"destination":"DE","zone":"europe","service":"express","priceCents":2995,"currency":"EUR","days":2}`, and the second a `422`: express does not go outside europe.

```bash
kdst invoke --stage development --function quote --local --path .koldstart/events/quote/quote-to-japan.json
```

`--local` builds the crate for this machine with `cargo build --release`, as cargo lambda builds for linux alone, and runs it against koldstart's emulator of the lambda runtime api. [.koldstart/events/quote](./.koldstart/events/quote) has an event for each case. `kdst remove --stage development` deletes the stage.

## testing it

```bash
cd cmd/quote && cargo test --locked
```

The tests run the quote and the handler against a fake rate card, with no aws account: the price of each kilogram or part of one, a parcel too heavy, to nowhere the card knows or by a service that does not go there, a body that cannot be read, a method and a path that are not the route's, and the headers of every answer. `cargo clippy --all-targets -- --deny warnings` lints the crate.

## related

- [http-routes](../../concepts/http-routes): the stage's http api, its routes, limits and access logs
- [outputs](../../concepts/outputs): `httpApiUrl`, which every stage with http routes gets
- [defaults](../../concepts/defaults): json logs and tracing for every function of a project
