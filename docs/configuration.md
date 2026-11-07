# configuration

A koldstart project is one [pkl](https://pkl-lang.org/) file, `koldstart.pkl` by default. It amends `Project.pkl`, the shape of the configuration, and imports `kdst.pkl`, which holds what goes inside functions and resources: the stage, `ref()`, the secrets, the events and their helpers, the resource shorthands and the authorizers.

```pkl
amends "@koldstart/Project.pkl"

import "@koldstart/kdst.pkl"

app = "orders"
region = "eu-west-1"
```

Amending it gives you type checking, defaults and completion in any editor with a pkl plugin. A mistake, such as a misspelled property or a memory size lambda does not offer, is reported with its line when the file is evaluated, before anything is built.

## PklProject

`@koldstart/` is the koldstart dependency of the pkl project the file is in: the `PklProject` beside it, or the nearest one above it, as in a repository with one project for several services. `kdst init` and `kdst migrate` write it:

```pkl
amends "pkl:Project"

dependencies {
  ["koldstart"] { uri = "package://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0" }
}
```

The schema package is released with koldstart, under the same version, on its GitHub release: the metadata at the package uri, and the zip it names. pkl resolves the `PklProject` into `PklProject.deps.json`, which pins the package by its checksum and is committed with it. koldstart resolves it before it evaluates the file when the lock file is missing or older than the `PklProject`, and says so on stderr; that downloads the package's metadata, and evaluating downloads the package, into pkl's cache, `~/.pkl/cache`.

A version that is not released, or a machine that is offline, stops there with an error that says which. `kdst upgrade` sets the dependency to the version of the koldstart you run, and resolves it again: see [upgrading](../README.md#upgrading).

### local and released schema

A release of koldstart writes the package uri of its own version. A build from source, `go install` or `make build` without a release tag, has no release to point at, so it writes a local dependency on the schema built into it instead:

```pkl
dependencies {
  ["koldstart"] = import("/home/you/.cache/koldstart/schema/9bd5ddef938b4d34/PklProject")
}
```

It extracts `Project.pkl`, `kdst.pkl` and their `PklProject` into `$XDG_CACHE_HOME/koldstart/schema/`, or `~/.cache/koldstart/schema/`, under a hash of the files, so a build with another schema gets a directory of its own. Resolving it downloads nothing. The path is of this machine: a project to share depends on a release.

A file without a `PklProject` that amends the full uri, `package://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0#/Project.pkl`, still evaluates, with no project. `@koldstart/` without a `PklProject`, or with one that has no koldstart dependency, is refused, and the error says to run `kdst upgrade`, which writes or completes it.

### schema version

The schema has a version of its own, `schemaVersion`, which `Project.pkl` renders: a koldstart refuses a file written against a newer schema than it reads, and says to upgrade. It goes up only when what the schema renders changes in a way an older koldstart cannot read, such as a new field it must act on; something an older one may ignore leaves it as it is.

## project

| property    | description                                                                                    |
| ----------- | ---------------------------------------------------------------------------------------------- |
| `app`       | name of the application: lowercase letters, digits and dashes, at most 24, starting with a letter |
| `region`    | aws region to deploy into; `--region` or `$AWS_REGION` takes precedence, and without any of them the aws profile's region is used |
| `stage`     | the stage from `--stage`, `dev` without it, read only; inside functions and resources, read it as `kdst.stage` |
| `functions` | the functions, by name                                                                          |
| `resources` | any other aws resources, by name; functions and resources share their names                     |
| `http`      | the stage's http api: domain, cors, authorizers, throttling and access logs                      |
| `websocket` | the stage's websocket api: `routeSelection`, see [the websocket api](#the-websocket-api)         |
| `outputs`   | values printed by `kdst outputs` and shown in `kdst ui`                                          |
| `tags`      | tags added to everything koldstart creates, next to its own                                      |

A function's or resource's name is letters, digits and dashes, at most 40, starting with a letter.

## functions

```pkl
functions {
  ["api"] {
    memory = 512
    timeout = 10.s
    environment {
      ["LOG_LEVEL"] = "info"
    }
  }
}
```

| property              | default   | description                                                                                       |
| --------------------- | --------- | ------------------------------------------------------------------------------------------------- |
| `runtime`             | inferred  | `nodejs22.x` or `nodejs24.x` for javascript and typescript, `provided.al2023` for go, `python3.12` or `python3.13` for python; none with an `image`; see [conventions](#conventions) |
| `handler`             | inferred  | for node and python, the file and its function, `src/api.handler`; for go, the package to build, `./cmd/worker`; none with an `image`; see [conventions](#conventions) |
| `image`               |           | a container image to build and run instead, see [container images](#container-images)              |
| `layers`              |           | lambda layers, by their version's arn, at most five; not for go or an image                         |
| `memory`              | `1024`    | megabytes, from `128` to `10240`                                                                   |
| `timeout`             | `6.s`     | a pkl duration, at most `15.min`                                                                   |
| `architecture`        | `"arm64"` | `arm64` or `x86_64`                                                                                |
| `ephemeralStorage`    | `512`     | the size of `/tmp` in megabytes, from `512` to `10240`                                             |
| `reservedConcurrency` |           | the concurrency kept for this function alone                                                        |
| `provisionedConcurrency` |        | instances kept started, on the function's `live` alias, see [versions](#versions)                   |
| `snapStart`           | `false`   | for python: start from a snapshot taken when a version is published, see [versions](#versions)      |
| `logRetention`        | `30`      | days the log group keeps logs, one of the periods cloudwatch allows                                 |
| `environment`         |           | variables, by a name of letters, digits and underscores: strings, `kdst.ref()` and `kdst.join()` values, secrets or outputs of other stages |
| `events`              |           | what invokes the function, see [events](#events)                                                    |
| `url`                 |           | the function's own url, see [function urls](#function-urls)                                         |
| `link`                |           | resources the function uses, see [link](#link)                                                      |
| `permissions`         |           | iam statements added to the function's role: `actions`, `resources` and `effect`, `"Allow"` unless `"Deny"` |
| `vpc`                 |           | `subnetIds` and `securityGroupIds` to put the function in                                           |
| `bundle`              |           | for node: `externals`, packages to leave out of the bundle, and `sourcemap`, which ships a source map and sets `NODE_OPTIONS=--enable-source-maps`; for python: `include`, what goes in the zip |
| `typecheck`           | `false`   | for typescript: run `tsc --noEmit` before bundling, which needs node and typescript installed       |
| `tracing`             | `false`   | send traces to x-ray                                                                               |
| `kmsKey`              |           | a kms key of your own, by its arn, that encrypts the variables in place of lambda's                 |
| `tags`                |           | tags of this function alone, next to the project's `tags`                                           |
| `deadLetter`          |           | an sqs queue or sns topic, by its arn, for the async events the function failed on every time        |
| `async`               |           | how lambda handles async events, see [failures](#failures)                                          |
| `fileSystem`          |           | an efs access point to mount: `accessPoint`, its arn, and `mountPath`, under `/mnt/`; needs `vpc`    |
| `logFormat`           | `"text"`  | `"json"` writes each line as json with its level, and takes the two levels below                    |
| `applicationLogLevel` |           | for json logs: the lowest level of the function's own lines kept, `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR` or `FATAL` |
| `systemLogLevel`      |           | for json logs: the lowest level of lambda's own lines kept, `DEBUG`, `INFO` or `WARN`               |

Each function gets a role and a log group of its own, `/aws/lambda/<app>-<stage>-<function>`. The role may write to the log group, and has what `link`, `permissions`, its events and its runtime secrets add, and what its settings need: writing traces, decrypting with its `kmsKey`, sending to its dead letter target and destinations, joining its `vpc` and mounting its file system.

`default`, pkl's default of a mapping, holds what every function gets:

```pkl
functions {
  default {
    memory = 256
    logRetention = 14
  }
  ["api"] {}
  ["worker"] { memory = 1024 }
}
```

A function that sets a property itself keeps its own value, as `worker` does with `memory`.

### conventions

A function without an `image` that leaves out `runtime` and `handler` gets them from its name and the files of the project, here for `["api"]`:

| file                                                                  | runtime           | handler           |
| --------------------------------------------------------------------- | ----------------- | ----------------- |
| `src/api.ts`, `.mts`, `.cts`, `.tsx`, `.js`, `.mjs`, `.cjs` or `.jsx` | `nodejs22.x`      | `src/api.handler` |
| `src/api.py`                                                          | `python3.13`      | `src/api.handler` |
| go files in `cmd/api/`                                                | `provided.al2023` | `./cmd/api`       |

A function that sets `runtime` alone gets that runtime's handler, so `runtime = "nodejs24.x"` runs `src/api.handler`. One that sets `handler` alone gets the runtime of what it names: go for `./services/api` or a directory of go files, otherwise node or python for the file that is there. A function's page in `kdst ui` shows the runtime it ends up with, and the handler lambda runs.

When none of the files is there, or more than one runtime fits, the configuration is refused before anything is built:

```
function api: no src/api.ts, src/api.py or cmd/api/ here: set runtime and handler
function api: src/api.ts and src/api.py are both here: set runtime to nodejs22.x or python3.13
```

`kdst migrate` writes both, as the handlers of a serverless.yml rarely follow the convention.

### building

Typescript and javascript are bundled with the esbuild that is built into koldstart, starting from the file in `handler`, so the zip holds one file, and its source map with `bundle.sourcemap`, and none of `node_modules`. Packages in `bundle.externals`, such as ones with native code, are left out of the bundle and copied from `node_modules`, with what they depend on; install them for the architecture the function runs on.

Go is built with `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, the `lambda.norpc` tag and `-ldflags "-s -w -buildid="`, for the architecture of the function, into a binary named `bootstrap`, which is what the `provided.al2023` runtime runs.

Python goes in the zip as it is: the top directory of the handler's module, `src` for `src/api.handler` and for `src/jobs/api.handler`, or the module alone when it is at the root, or what `bundle.include` names instead. Its dependencies come from `requirements.txt`, or from `pyproject.toml` through `uv export`, locked by `uv.lock` when there is one, and are installed with [uv](https://docs.astral.sh/uv/) for the platform lambda runs: manylinux wheels for the function's architecture, so a package with native code works however you build it. Pin the versions, so every build installs the same.

Zips are deterministic: the same code gives the same bytes, on a laptop and in ci, so the hash of the zip tells koldstart whether a function's code changed. Go binaries also depend on the version of go that builds them, so pin it with a `toolchain` line in `go.mod`, such as `toolchain go1.27.3`; otherwise a laptop and ci with different versions upload different code.

### container images

```pkl
["thumbnails"] {
  image { dockerfile = "thumbnails/Dockerfile"; context = "thumbnails" }
  memory = 2048
}
```

A function with an `image` runs that instead of a runtime, and has no `runtime`, `handler` or `layers`. koldstart builds it with docker for the function's architecture, from `dockerfile`, `Dockerfile` without one, in `context`, the project's directory without one, and pushes it to the ecr repository `koldstart/<app>` of the account and region, which it creates on the first deploy; `buildArgs` are passed as `--build-arg`, and `command`, `entryPoint` and `workingDirectory` set what lambda runs in place of the image's own. The image is tagged by a hash of what it is built from: every file of the context but what `.dockerignore` leaves out, the dockerfile, the build arguments and the platform. A deploy that changes none of them builds and pushes nothing, and leaves the function as it is.

The repository keeps the images of every deploy, as lambda stops a function whose image is gone; images that lost their tag expire after a day. Build it from one of lambda's base images, such as `public.ecr.aws/lambda/python:3.12`, so it has the runtime client, which also lets `invoke --local` run it here with docker.

### link

`link` lets a function use a resource of the project, `"read"`, `"write"` or `"readwrite"`: its role gets a policy for the access, and the resource's name and arn become environment variables named after it.

```pkl
link {
  ["orders"] = "read"
  ["jobs"] = "readwrite"
}
```

| type                          | variables                     |
| ----------------------------- | ----------------------------- |
| `AWS::DynamoDB::Table`        | `<NAME>_NAME`, `<NAME>_ARN`   |
| `AWS::S3::Bucket`             | `<NAME>_NAME`, `<NAME>_ARN`   |
| `AWS::SQS::Queue`             | `<NAME>_URL`, `<NAME>_ARN`    |
| `AWS::SNS::Topic`             | `<NAME>_ARN`                  |
| `AWS::Kinesis::Stream`        | `<NAME>_NAME`, `<NAME>_ARN`   |
| `AWS::SecretsManager::Secret` | `<NAME>_ARN`                  |
| `AWS::SSM::Parameter`         | `<NAME>_NAME`                 |

`<NAME>` is the resource's name in uppercase, dashes and dots as underscores: `orders` gives `ORDERS_NAME`. A type that is not in this table is refused; give access to it with `permissions`:

```pkl
permissions {
  new { actions { "events:PutEvents" }; resources { kdst.ref("bus", "Arn") } }
}
```

### versions

A function with `provisionedConcurrency` or `snapStart` gets a version on every deploy that changes it, and an alias, `live`, pointed at it. Its events, its url and `kdst invoke` call the alias, so they run what was deployed last, on the instances kept started for it, or from its snapshot:

```pkl
functions {
  ["api"] { provisionedConcurrency = 2 }
  ["report"] { snapStart = true }
}
```

Lambda publishes no version when nothing changed since the last one. koldstart keeps the last three, to point the alias back at with `aws lambda update-alias`, and deletes the ones before. Other functions get no versions, and their events call the function itself.

Giving an existing function either one moves its events to the alias, and its url to a new one, of the alias. Taking both away deletes the alias and moves them back. `snapStart` is for python, and takes no `provisionedConcurrency`, `fileSystem` or more than `512` megabytes of `ephemeralStorage`. `provisionedConcurrency` is at most the function's `reservedConcurrency`, when it has one. Instances kept started cost money while they wait, whether they are invoked or not.

### failures

When a function fails on an event lambda handed it without waiting, as s3, sns and eventbridge do, lambda tries it again. `async` says how often, how long an event may wait, and where results go:

```pkl
deadLetter = kdst.ref("failed", "Arn")
async = new {
  maximumRetryAttempts = 0
  maximumEventAge = 1.h
  onSuccess = kdst.ref("settle", "Arn")
  onFailure = kdst.ref("failed", "Arn")
}
```

`maximumRetryAttempts` is `0` to `2`, and `maximumEventAge` a minute to six hours; lambda's defaults are `2` and six hours. A destination is an sqs queue, an sns topic, an event bus or a function, by its arn; `kdst.ref()` of one of the project's. The role may send to each one koldstart can tell the service of; one given as a value only known while deploying, such as `kdst.stageOutput()`, needs a statement in `permissions`.

A stream's mapping takes how it handles a batch the function fails on, and an event rule how it hands over events:

```pkl
(kdst.fromStream("clicks")) {
  maximumRetryAttempts = 3
  maximumRecordAge = 1.h
  bisectBatchOnFunctionError = true
  onFailure = kdst.ref("failed", "Arn")
}
(kdst.fromEventBridge(new Dynamic { source { "orders" } })) {
  maximumRetryAttempts = 4
  maximumEventAge = 2.h
  deadLetter = kdst.ref("failed", "Arn")
}
```

A stream without `maximumRetryAttempts` or `maximumRecordAge` retries a batch until its records expire; `maximumRecordAge` is a minute to a week, and its `onFailure` an sqs queue, an sns topic or an s3 bucket, by its arn. A rule tries again at most `185` times, for a minute to a day. A rule's `deadLetter` is an sqs queue whose policy lets `events.amazonaws.com` send to it; koldstart does not write that policy.

### function urls

```pkl
url = new { auth = "iam" }
```

`auth` has no default: `"iam"` requires signed requests, `"none"` makes the url public. `cors` takes `origins`, `methods`, `headers`, `expose`, `credentials` and `maxAge`, a duration, as the http api's does. The url is the output `<function>Url`, the function's name in camel case: `stripeWebhookUrl` for `stripe-webhook`.

## events

Events are added to a function's `events`, with the helpers of `kdst.pkl`:

```pkl
events {
  kdst.get("/orders/{id}")
  (kdst.post("/orders")) { authorizer = "users" }
  kdst.schedule("rate(5 minutes)")
  (kdst.fromQueue("jobs")) { batchSize = 5 }
  kdst.fromTopic("alerts")
  kdst.fromTableStream("orders")
  (kdst.fromBucket("uploads")) { prefix = "incoming/" }
  (kdst.fromEventBridge(new Dynamic { source { "orders.service" } })) { bus = "orders" }
  kdst.fromWebSocket("$connect")
  (kdst.fromLogs("/ecs/web")) { filter = "ERROR" }
}
```

| helper                                   | gives                                                                  |
| ---------------------------------------- | ---------------------------------------------------------------------- |
| `kdst.get(path)`, `post`, `put`, `patch`, `del`, `head`, `options`, `any` | an `HttpRoute` of that method; `del` as `delete` is a keyword of pkl |
| `kdst.schedule(expression)`              | a `Schedule`                                                           |
| `kdst.fromQueue(name or arn)`            | a `Queue`; a name is `kdst.ref(name, "Arn")`                            |
| `kdst.fromTopic(name or arn)`            | a `Topic`; a name is `kdst.ref(name, "TopicArn")`                       |
| `kdst.fromStream(name or arn)`           | a `Stream` of a kinesis stream; a name is `kdst.ref(name, "Arn")`       |
| `kdst.fromTableStream(name or arn)`      | a `Stream` of a dynamodb table's stream; a name is `kdst.ref(name, "StreamArn")` |
| `kdst.fromBucket(name or arn)`           | a `Bucket`; a name is `kdst.ref(name, "BucketName")`, and `arn:aws:s3:::<name>` any other bucket |
| `kdst.fromEventBridge(pattern)`          | an `EventRule` on the default bus                                       |
| `kdst.fromWebSocket(route)`              | a `WebSocketRoute`                                                      |
| `kdst.fromLogs(log group)`               | a `LogSubscription` of a log group, by its name, as it is              |

A name is a resource of the project; anything that starts with `arn:`, or a value such as `kdst.stageOutput()`, is passed as it is. Each helper gives the event's class, so the rest of the class amends it, in parentheses. The classes are the long form, and the only one for a load balancer:

```pkl
events {
  new kdst.HttpRoute { method = "GET"; path = "/orders/{id}" }
  new kdst.Schedule { expression = "rate(5 minutes)" }
  new kdst.Queue { queue = kdst.ref("jobs", "Arn"); batchSize = 10 }
  new kdst.Topic { topic = kdst.ref("alerts", "TopicArn") }
  new kdst.Stream { stream = kdst.ref("orders", "StreamArn"); startingPosition = "LATEST" }
  new kdst.Bucket { bucket = kdst.ref("uploads", "BucketName"); events { "s3:ObjectCreated:*" }; prefix = "incoming/" }
  new kdst.EventRule { bus = "orders"; pattern = new Dynamic { source { "orders.service" }; ["detail-type"] { "OrderPlaced" } } }
  new kdst.WebSocketRoute { route = "$connect" }
  new kdst.LoadBalancer { listener = "arn:aws:elasticloadbalancing:..."; priority = 10; paths { "/legacy/*" } }
  new kdst.LogSubscription { logGroup = "/ecs/web"; filter = "ERROR" }
}
```

| class       | creates                                                                                            |
| ----------- | -------------------------------------------------------------------------------------------------- |
| `HttpRoute` | a route on the stage's http api, of a `path` that starts with `/` or is `$default`, with its integration and permission; `authorizer` and `scopes` guard it, or `iam = true` takes only requests signed by a caller that may `execute-api:Invoke` it, not both; a method and path belong to one function |
| `Schedule`  | an eventbridge scheduler schedule, `rate(...)`, `cron(...)` or `at(...)`, with `timezone`, `UTC` without one, `input` and `enabled` |
| `Queue`     | an event source mapping from an sqs queue, with `batchSize`, `10` without one, `maximumBatchingWindow`, `reportBatchItemFailures`, `filters`, `maximumConcurrency` and `enabled` |
| `Topic`     | an sns subscription and the permission for sns to invoke the function, with `filterPolicy`          |
| `Stream`    | an event source mapping from a kinesis stream or a dynamodb stream, with `startingPosition`, `LATEST` or `TRIM_HORIZON`, `batchSize`, `100` without one, `maximumBatchingWindow`, `reportBatchItemFailures`, `filters`, `enabled`, `parallelizationFactor`, `tumblingWindow`, and its [failures](#failures) |
| `Bucket`    | the function in the bucket's notifications of `events`, `s3:ObjectCreated:*` without any, for the keys of `prefix` and `suffix`, and the permission for s3 to invoke it |
| `EventRule` | an eventbridge rule on `bus`, `default` without one, that sends the events matching `pattern`, an object or json, to the function, with `input`, `inputPath` or `inputTransformer` in their place, `enabled` and its [failures](#failures); and the permission for eventbridge to invoke it |
| `WebSocketRoute` | a route of the stage's websocket api, `$connect`, `$disconnect`, `$default` or a value of the route selection, with its integration and permission |
| `LoadBalancer` | a target group of the function, the permission for the load balancer to invoke it, and a rule of `priority` on `listener` that forwards the requests matching its `paths`, `hosts`, `methods`, `headers`, `query` and `sourceIps`, at least one of them, to it; `multiValueHeaders = true` hands it headers and query parameters as lists |
| `LogSubscription` | a subscription filter of `logGroup` that sends the lines matching `filter`, every line without one, to the function, and the permission for cloudwatch logs to invoke it; a log group takes two |

A queue must hide a message for at least the function's timeout, its `VisibilityTimeout`, or lambda refuses the mapping; koldstart checks it for queues of the project. `reportBatchItemFailures` is on by default, so a function returns the messages that failed and only those are retried.

A bucket has one notification configuration, so koldstart puts the stage's functions in it and leaves every other notification as it found it. A bucket of the project that sets `NotificationConfiguration` itself cannot also have bucket events.

### the http api

All routes of a stage share one http api, made once a function has a route. Its url is the output `httpApiUrl`.

```pkl
http {
  domain = "api.example.com"
  certificate = "arn:aws:acm:eu-west-1:123456789012:certificate/..."
  cors = new { origins { "https://example.com" } }
  throttling = new { rate = 100; burst = 200 }
  accessLogs = new { retention = 14 }
  authorizers {
    ["users"] = new kdst.JwtAuthorizer { issuer = "https://auth.example.com"; audience { "orders" } }
    ["keys"] = new kdst.LambdaAuthorizer { functionName = "authorize"; ttl = 5.min }
  }
}
```

With a domain, `httpApiUrl` is `https://<domain>`, and the output `httpDomainTarget` is where its dns record points. `cors` takes what a [function url's](#function-urls) does. A route picks an authorizer with `authorizer = "users"`, and sets a request limit of its own, in place of the stage-wide one, with `throttling = new { rate = 5; burst = 10 }`.

A `JwtAuthorizer` checks a token from `issuer`, an `https://` url, for one of `audience`. A `LambdaAuthorizer` is a function of the project, by its name, that reads what `identity` lists, such as `$request.header.Authorization`, and whose answer is cached for `ttl`, at most `1.h`.

`accessLogs` writes a line for every request to a log group koldstart makes, `/aws/http-api/<app>-<stage>`, kept for `retention` days, `30` without it. The line is json of the request's id, ip, time, method, route, status, protocol and length, or what `format` says with api gateway's `$context` variables.

### the websocket api

The `WebSocketRoute`s of a stage share one websocket api, with a stage of the stage's name, and each route belongs to one function. Its url is the output `webSocketUrl`. A message picks its route by its `action`, `$request.body.action`, or what `websocket.routeSelection` says:

```pkl
websocket { routeSelection = "$request.body.type" }
```

A function with websocket routes may post to the connections of the api, and gets the url to post to as `WEBSOCKET_CALLBACK_URL`, for the api management client of the aws sdk.

## resources

Anything that is not a function goes in `resources`. A resource has a CloudFormation type name and the properties CloudFormation documents for it, and koldstart creates, updates and deletes it through the [cloud control api](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/what-is-cloudcontrolapi.html), without a stack. The [resource type reference](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-template-resource-type-ref.html) lists the types and their properties; `type` is an `AWS::` one, such as `AWS::DynamoDB::Table`. A type the registry does not know, or one cloud control cannot create, is refused before anything is planned.

`kdst.pkl` has shorthands for the common ones:

```pkl
resources {
  ["orders"] = kdst.table("id")
  ["events"] = (kdst.table("customer")) { sortKey = "createdAt"; sortKeyType = "N" }
  ["jobs"] = (kdst.queue()) { properties { VisibilityTimeout = 60 } }
  ["uploads"] = (kdst.bucket()) { properties { VersioningConfiguration { Status = "Enabled" } } }
  ["alerts"] = kdst.topic()
}
```

| shorthand                 | type                   | properties                                                                  |
| ------------------------- | ---------------------- | --------------------------------------------------------------------------- |
| `kdst.table(partitionKey)` | `AWS::DynamoDB::Table` | on-demand billing, point-in-time recovery, the partition key a string; `sortKey`, `partitionKeyType` and `sortKeyType`, `S`, `N` or `B`, amend it |
| `kdst.queue()`            | `AWS::SQS::Queue`      | messages kept 14 days, the most sqs allows, rather than 4                    |
| `kdst.bucket()`           | `AWS::S3::Bucket`      | all public access blocked, objects encrypted with keys of s3 (`AES256`)      |
| `kdst.topic()`            | `AWS::SNS::Topic`      | none                                                                        |

A shorthand is a resource like any other: `properties { ... }` adds to its properties or changes one, `deletionPolicy` and the rest amend it, and `link` and `kdst.ref()` work on it as on one written out. To replace what it sets rather than add to it, such as a bucket's encryption with a kms key, assign it with `=`: `properties { BucketEncryption = new { ... } }`. The long form is the type and the properties:

```pkl
resources {
  ["uploads"] {
    type = "AWS::S3::Bucket"
    properties {
      VersioningConfiguration { Status = "Enabled" }
    }
  }
}
```

Before anything is planned, the properties are checked against the type's schema in the cloudformation registry, as cloud control would check them on create, so a mistake in any resource stops the plan with all of them listed:

```text
resources[orders] does not fit the schema of AWS::DynamoDB::Table:
  properties.BillingMod: no such property: did you mean BillingMode?
  properties.KeySchema: required, and not set
```

It reports properties the type does not have, values of the wrong type, outside an enum, a pattern, a length or a range, required properties that are not set, and properties aws sets itself, such as `Arn`, which are read with `kdst.ref()` instead. A `kdst.ref()`, a secret or an output is known only while applying, so it is not checked. Where the registry cannot be read, as on an emulator, the check is left to cloud control.

A change to a property is sent as a patch of what changed. A change to a property the type cannot change in place, such as a table's key schema, replaces the resource.

### keeping data

Resources that hold data are retained: `kdst remove` leaves them in place, and the next deploy of the stage takes them over again. They are tables, buckets, databases, file systems, streams, keys, secrets, user pools, repositories, caches, search domains and backup vaults. `deletionPolicy` decides for one resource:

```pkl
["cache"] = (kdst.table("key")) { deletionPolicy = "delete" }
```

A plan that would replace a retained resource stops, because the new one starts empty, and says which properties need a new one. `kdst plan --allow-replace` and `kdst deploy --allow-replace` replace it, and leave the old one in place; when the new one needs the old one's name, give it another.

### taking over a resource

A resource made outside koldstart is taken over with `kdst import`, by the identifier cloud control knows it by: a table's name, a queue's url, a bucket's name.

```bash
kdst import orders orders-prod
```

The resource must be in `resources`. `import` reads it into the state and changes nothing in aws; the next deploy patches the properties the configuration sets that differ, and leaves the rest. An imported resource is retained unless its `deletionPolicy` says delete.

## references

`kdst.ref(name, attribute)` stands for an attribute of a function or resource of the project, known once it exists, such as an arn or a generated name. koldstart resolves it while applying, and applies what it refers to first.

```pkl
environment {
  ["BUCKET"] = kdst.ref("uploads", "BucketName")
}
```

The attributes of a resource are its properties as cloud control returns them, such as `Arn` and `TableName`. A function has `Arn` and `FunctionName`, and one with [versions](#versions) also `AliasArn` and `AliasName`, of its `live` alias. A ref to a name that is neither, or a resource's ref to itself, is refused.

A ref is a value of its own, not a string, so it cannot sit inside a pkl string. `kdst.join()` joins strings, refs and the account's values into one, once the refs are known:

```pkl
environment {
  ["INDEX_ARN"] = kdst.join(List(kdst.ref("orders", "Arn"), "/index/*"))
  ["TOPIC_ARN"] = kdst.join(List("arn:", kdst.partition, ":sns:", module.region, ":", kdst.accountId, ":alerts"))
}
```

`kdst.accountId`, `kdst.partition` and `kdst.urlSuffix` are the account the stage deploys to, the partition of its region, `aws` outside china and govcloud, and the domain of aws's endpoints there. They go wherever a value goes, and in `kdst.join()`.

## outputs

```pkl
outputs {
  ["table"] = kdst.ref("orders", "TableName")
}
```

An output is a string, a `kdst.ref()`, a `kdst.join()` or a value of the account; a secret or `kdst.stageOutput()` is not one. Every deploy resolves them, records them in the state and prints them, with `httpApiUrl`, `webSocketUrl`, `httpDomainTarget` with a domain, and a `<function>Url` for each function url. `kdst outputs` prints what the last deploy recorded, one `name = value` a line, and `-o json` prints them as one json object, `{}` when there are none:

```bash
kdst outputs -o json | jq -r .table
```

## stages

`--stage` or `$KOLDSTART_STAGE` picks the stage, `dev` without either, and every stage is deployed separately, with its own resources, state and lock. A stage is lowercase letters, digits and dashes, at most 16, starting with a letter. `kdst.stage` is readable anywhere in the file, so one file describes all of them:

```pkl
memory = if (kdst.stage == "prod") 1024 else 256
```

A stage that differs a lot can have its own file that amends the main one, passed with `--config`:

```pkl
amends "koldstart.pkl"

region = "us-east-1"
```

One app reads another's outputs with `kdst.stageOutput(app, stage, output)`, from the state of that stage in the same account and region. A plan fails when that stage is not deployed, or has no such output. A `kdst.stageOutput()` of this app and stage is refused: use `kdst.ref()`.

An output of a stage in another region or account amends it with `region`, `account` and `role`:

```pkl
environment {
  ["PAYMENTS_URL"] = (kdst.stageOutput("payments", "prod", "apiUrl")) { region = "us-east-1" }
  ["LEDGER_URL"] = (kdst.stageOutput("ledger", "prod", "apiUrl")) {
    role = "arn:aws:iam::210987654321:role/koldstart-outputs"
  }
}
```

The state is read from the stage's bucket, `koldstart-state-<account>-<region>`, at `<app>/<stage>/state.json`, and only from a bucket that account owns. With a `role`, koldstart assumes it to read it, and the stage is in the role's account unless `account` says another. The role needs `s3:GetObject` on that key, and the deploying credentials need `sts:AssumeRole` on the role. Without a role, the deploying credentials read it themselves, which the bucket's policy has to allow for another account.

## secrets

`kdst.param("name")` is read from ssm parameter store, and `kdst.secret("name")` from secrets manager, each under the app and stage; `.field("key")` takes one key of a json secret. They are read while planning and deploying, so a plan shows when one changed, and their values become environment variables of the function. Their values never go into the state.

```pkl
environment {
  ["API_KEY"] = kdst.param("api-key")
  ["DB_USER"] = kdst.secret("db").field("username")
  ["DB_PASSWORD"] = kdst.secret("db").field("password").readAtRuntime()
  ["SENTRY_DSN"] = kdst.ssm("/shared/sentry-dsn")
}
```

Each stage has its own parameters and secrets, named after the app and the stage:

| helper                  | reads, for app `orders` on stage `prod`     |
|-------------------------|---------------------------------------------|
| `kdst.param("api-key")` | the ssm parameter `/orders/prod/api-key`    |
| `kdst.secret("db")`     | the secrets manager secret `orders/prod/db` |

The name has no leading `/`, no `..` and no empty segments, and the plan and `kdst ui` show the full name, as `(secret ssm:/orders/prod/api-key)`.

For a parameter or secret by its full name or arn, such as one that every stage or another app shares, use `kdst.ssm("/path")` and `kdst.secretsmanager("name")`. They read the name as it is: `kdst.ssm("api-key")` reads the parameter `api-key`, and `kdst.ssm("/orders/\(kdst.stage)/api-key")` the one `kdst.param("api-key")` does.

The values are visible to anyone who can read the function's configuration. With `.readAtRuntime()`, the variable holds the arn of the parameter or secret instead, the function's role may read it, and the function reads it itself; with a `.field()`, `<NAME>_KEY` says which key, here `DB_PASSWORD_KEY=password`. A parameter encrypted with a kms key of your own also needs `kms:Decrypt` on it, through `permissions`.
