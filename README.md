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

`stage` is whatever `--stage` was set to, so one file describes every stage, and here the api gets more memory in `prod`. `resources` takes any `AWS::*` type, created through the cloud control api. `ref()` reads an attribute of a resource, such as the table's name, once it exists, and `${ssm:...}` is read from parameter store during the deploy.

```bash
kdst plan --stage prod
kdst deploy --stage prod
```

`plan` shows what would change and touches nothing. `deploy` builds the functions, uploads them and applies the plan.

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

## getting started

`init` writes a `koldstart.pkl` and an example function into the current directory:

```bash
mkdir orders && cd orders
kdst init
```

Edit the file to describe your functions, or start from the example at the top of this page. Without `--stage`, every command works on `dev`. The first plan lists everything as a create: each function with its iam role and log group, the http api with its routes, the queue mapping, the table and the queue.

```bash
kdst plan
kdst deploy
```

The first deploy in an account and region also creates the state bucket. When it finishes, `outputs` prints the url of the http api:

```bash
kdst outputs
```

Invoke a function with an event from a file or inline, and follow its logs:

```bash
kdst invoke -f api --path events/get-order.json
kdst logs -f api --tail
```

After changing only a function's code, deploy just that function. It skips planning and replaces the code in place:

```bash
kdst deploy function -f api
```

`remove` deletes everything the stage deployed, after asking you to type the stage name:

```bash
kdst remove
```

## stages

Every stage is deployed separately, with its own functions, resources, state and lock, so `dev`, a stage per pull request and `prod` can live in the same account without touching each other. Small differences go in the main file through `stage`. A stage that differs a lot gets its own file that amends the main one, such as a `koldstart.prod.pkl` with:

```pkl
amends "koldstart.pkl"

region = "us-east-1"
```

```bash
kdst deploy --stage prod --config koldstart.prod.pkl
```

## secrets

`${ssm:/path}` reads a value from ssm parameter store, `${secretsmanager:name}` reads a secret from secrets manager, and `${secretsmanager:name:key}` takes one key from a json secret. They are resolved during the deploy and become environment variables of the function, so anyone who can read the function's configuration can read them too.

To keep them out of the configuration, set `secretsAtRuntime = true` on the function. The environment variable then holds the arn of the parameter or secret, the function's role is allowed to read it, and the function reads it itself when it starts.

## local development

`invoke --local` runs one function on your machine, built the same way as for a deploy, against an emulator of the lambda runtime api:

```bash
kdst invoke -f worker --local --path events/job.json
```

`offline` serves every http route of the project locally, so the api can be called with curl or a browser while you work on it:

```bash
kdst offline
```

The events you invoke functions with in `kdst ui` are saved as json under `.koldstart/events/<function>/`, so they can be committed and shared with the rest of the team.

## deploying from ci

A ci job needs aws credentials and nothing else: the state bucket and the lock are already shared with everyone who deploys the app. With GitHub Actions and an iam role it can assume through oidc:

```yaml
name: deploy

on:
  push:
    branches: [main]

permissions:
  id-token: write
  contents: read

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::123456789012:role/koldstart-deploy
          aws-region: eu-west-1
      - run: |
          docker run --rm -v "$PWD:/work" -w /work \
            -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN -e AWS_REGION \
            ghcr.io/kontrolplane/koldstart:latest deploy --stage prod
```

The role needs permission to manage what the configuration describes, and to read and write the state bucket. When a job is cancelled halfway, running it again finishes the deploy. When the runner itself was killed and the lock was left behind, `kdst unlock --stage prod` releases it.

## what ends up in your account

- for each function: the lambda function, an iam role and a log group, and the permissions its events need to invoke it
- for each stage with http routes: one api gateway http api that all its routes share
- everything in `resources`, created through the cloud control api
- per account and region: one bucket, `koldstart-state-<account>-<region>`, versioned, encrypted and with public access blocked, holding the state, locks and zips of every app and stage

Everything koldstart creates is tagged, together with the `tags` from the configuration. Nothing else is created, and nothing is sent anywhere outside your account.

## not supported yet

- runtimes other than node and go
- the rest api of api gateway, only http apis are deployed
- lambda layers and container image functions
- taking over resources that koldstart did not create, which is an open question in [#1](https://github.com/kontrolplane/koldstart/issues/1)

## commands

| command                    | description                                                                 |
| -------------------------- | --------------------------------------------------------------------------- |
| `init`                     | write a `koldstart.pkl` and an example function to start from               |
| `package`                  | build and zip every function, without deploying                             |
| `plan`                     | show what a deploy would create, change and delete, `--refresh` to include drift |
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
