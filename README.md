<p align="center">
  <a href="https://kontrolplane.dev">
    <img width="1500" alt="kontrolplane koldstart" src="./assets/kontrolplane-header.svg">
  </a>
</p>

`koldstart` deploys serverless applications to `AWS Lambda`. You describe your functions, the events that trigger them and the resources they use in a [pkl](https://pkl-lang.org/) file, and koldstart builds, packages and deploys them into your own AWS account. It is free to use without limits: no license key, no hosted dashboard, no telemetry and nothing running outside your account. `kdst` is the short name for the same binary.

---

> [!NOTE]
> koldstart is in the design phase and nothing here works yet. The plan is in [#1](https://github.com/kontrolplane/koldstart/issues/1), and the documentation describes the design as it stands.

Here is an http api in typescript and a queue worker in go, sharing a table:

```pkl
amends "package://github.com/kontrolplane/koldstart/releases/download/koldstart@0.1.0/koldstart@0.1.0#/Project.pkl"

import "package://github.com/kontrolplane/koldstart/releases/download/koldstart@0.1.0/koldstart@0.1.0#/Events.pkl"

app = "orders"
region = "eu-west-1"

functions {
  ["api"] {
    runtime = "nodejs22.x"
    handler = "src/api.handler"
    // stage comes from --stage, so one file describes every stage
    memory = if (stage == "prod") 1024 else 256
    environment {
      ["TABLE_NAME"] = ref("orders", "TableName")
      ["API_KEY"] = "${ssm:/orders/api-key}"
    }
    events {
      new Events.HttpRoute { method = "GET"; path = "/orders/{id}" }
      new Events.HttpRoute { method = "POST"; path = "/orders" }
    }
  }
  ["worker"] {
    runtime = "provided.al2023"
    handler = "./cmd/worker"
    environment {
      ["TABLE_NAME"] = ref("orders", "TableName")
    }
    events {
      new Events.Queue { queue = ref("jobs", "Arn"); batchSize = 10 }
    }
  }
}

// any AWS::* type, created through the cloud control api
resources {
  ["orders"] {
    type = "AWS::DynamoDB::Table"
    properties {
      BillingMode = "PAY_PER_REQUEST"
      AttributeDefinitions { new { AttributeName = "id"; AttributeType = "S" } }
      KeySchema { new { AttributeName = "id"; KeyType = "HASH" } }
    }
  }
  ["jobs"] {
    type = "AWS::SQS::Queue"
  }
}
```

```bash
kdst plan --stage prod     # what would change, touching nothing
kdst deploy --stage prod   # build, upload and apply
```

## highlights

koldstart calls the AWS APIs directly instead of generating CloudFormation, so there is no stack to wait on and no 500 resource limit. When the only change is a function's code, a deploy uploads the new zip and calls `UpdateFunctionCode`, which takes seconds. `kdst deploy function -f api` goes straight down that path.

What was deployed is recorded in an S3 bucket in your account, created on the first deploy. A team and its CI share it without setting anything up, and a lock in the same bucket keeps two deploys of one stage from running at once. State is written after every step, so a deploy that fails halfway picks up where it stopped the next time it runs.

Functions are written in typescript, javascript or go. Typescript is bundled with esbuild built into the binary, so packaging needs no node install. Go is cross-compiled to a `bootstrap` binary for the `provided.al2023` runtime. Zips are built byte for byte the same from the same input, so their hash tells koldstart whether the code changed.

Functions are triggered by api gateway http routes, function urls, schedules, sqs, sns, kinesis and dynamodb streams, and s3 notifications. Anything else AWS offers goes in `resources`, using the same type names and properties as CloudFormation, and `ref()` reads a resource's attributes into a function's configuration.

`kdst invoke --local` and `kdst offline` run functions on your machine against a small emulator of the lambda runtime api, so a handler runs locally the way it runs in AWS. `kdst ui` is a terminal interface for everything deployed: apps, stages, functions, resources, logs, invocations, deploys and locks.

Coming from the serverless framework, `kdst migrate` converts a `serverless.yml` into a `koldstart.pkl` and lists what it could not translate.

## installation

```bash
brew install kontrolplane/tap/koldstart
```

The formula installs `koldstart` and a `kdst` link to it. Or with go, which requires [go](https://go.dev/) 1.26 or later:

```bash
go install github.com/kontrolplane/koldstart/cmd/koldstart@latest
```

`go install` installs `koldstart` only, `alias kdst=koldstart` gives you the short name.

Prebuilt binaries for linux, macos and windows are attached to the [GitHub releases](https://github.com/kontrolplane/koldstart/releases), together with a `checksums.txt` to verify the download.

A container image for linux/amd64 and linux/arm64 is published to `ghcr.io/kontrolplane/koldstart`. Releases are tagged with their version and `latest`, and `main` tracks the latest commit on main:

```bash
docker run --rm -it -v "$PWD:/work" -w /work -v "$HOME/.aws:/root/.aws:ro" -e AWS_PROFILE ghcr.io/kontrolplane/koldstart:latest plan
```

koldstart evaluates configuration with the `pkl` binary. Homebrew installs it as a dependency. Otherwise koldstart downloads a pinned version to `~/.cache/koldstart` on first use and checks it against its published checksum, or uses the one `KOLDSTART_PKL` points at.

## commands

| command                    | description                                                                 |
| -------------------------- | --------------------------------------------------------------------------- |
| `init`                     | write a `koldstart.pkl` and an example function to start from               |
| `package`                  | build and zip every function, without deploying                             |
| `plan`                     | show what a deploy would create, change and delete                          |
| `deploy`                   | build, upload and apply the plan                                            |
| `deploy function -f <fn>`  | deploy only the code of one function                                        |
| `remove`                   | delete everything the stage deployed                                        |
| `outputs`                  | print the stage's outputs, such as the http api url                         |
| `logs -f <fn>`             | print a function's logs, `--tail` to follow them                            |
| `invoke -f <fn>`           | invoke a function with `--data` or `--path`, `--local` to run it on your machine |
| `offline`                  | serve the http routes locally                                               |
| `migrate [serverless.yml]` | convert a serverless framework project                                      |
| `unlock`                   | release a stage's lock left by a deploy that did not finish                 |
| `ui`                       | open the terminal interface                                                 |
| `version`                  | print the version                                                           |

## flags

| flag             | environment variable | description                                                    |
| ---------------- | -------------------- | -------------------------------------------------------------- |
| `--stage`, `-s`  | `KOLDSTART_STAGE`    | stage to work on, defaults to `dev`                            |
| `--config`, `-c` | `KOLDSTART_CONFIG`   | configuration file, defaults to `koldstart.pkl`                |
| `--profile`      | `AWS_PROFILE`        | aws profile from the shared aws config                         |
| `--region`       | `AWS_REGION`         | aws region, takes precedence over `region` in the configuration |
| `--debug`        |                      | write debug logs to `debug.log`                                |
| `--version`      |                      | print the version and exit                                     |
| `--help`, `-h`   |                      | print the flags and exit                                       |

Credentials come from the standard aws chain: environment variables, the shared config and credentials files, sso, and the role of the machine or ci job koldstart runs on.

## documentation

- [configuration](./docs/configuration.md): the pkl schema, functions, events, resources, references, stages and secrets
- [how it works](./docs/how-it-works.md): planning, applying, state, locking and what happens when a deploy fails
- [terminal interface](./docs/ui.md): the views and keybindings of `kdst ui`
- [migrating from the serverless framework](./docs/migrating.md): what `kdst migrate` converts and what it leaves to you

## development

- [go](https://go.dev/) 1.26+
- [pkl](https://pkl-lang.org/)
- [node](https://nodejs.org/) 22+, only for `invoke --local` and `offline` with javascript functions
- an AWS account to deploy into, only for the end to end tests

The unit tests run against fake providers and an in-memory state backend, so they need no AWS account:

```bash
go test ./...
```

The end to end tests deploy [examples/full-featured](./examples/full-featured) into the account your credentials point at, call it, change its code, check that only the code was updated and remove it again:

```bash
AWS_PROFILE=sandbox go test -tags e2e ./test/e2e/...
```

## license

[Apache-2.0](./LICENSE) © kontrolplane.

## contributors

[//]: kontrolplane/generate-contributors-list

<a href="https://github.com/levivannoort"><img src="https://avatars.githubusercontent.com/u/73097785?v=4" title="levivannoort" width="50" height="50"></a>

[//]: kontrolplane/generate-contributors-list

</br>

<p align="center">
  <img width="1500" alt="" src="./assets/kontrolplane-footer.svg">
</p>
