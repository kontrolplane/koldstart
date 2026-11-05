# configuration

A koldstart project is one [pkl](https://pkl-lang.org/) file, `koldstart.pkl` by default. It amends `Project.pkl`, the shape of the configuration, and imports `kdst.pkl`, which holds what goes inside functions and resources: the stage, `ref()`, the secrets, the events and the authorizers.

```pkl
amends "package://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0#/Project.pkl"

import "package://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0#/kdst.pkl"

app = "orders"
region = "eu-west-1"
```

Amending it gives you type checking, defaults and completion in any editor with a pkl plugin. A mistake, such as a misspelled property or a memory size lambda does not offer, is reported with its line when the file is evaluated, before anything is built.

The package is released with koldstart, under the same version. Its schema has a version of its own: a koldstart refuses a file written against a newer schema than it reads, and says to upgrade.

## project

| property    | description                                                                                    |
| ----------- | ---------------------------------------------------------------------------------------------- |
| `app`       | name of the application: lowercase letters, digits and dashes, at most 24, starting with a letter |
| `region`    | aws region to deploy into; `--region` takes precedence                                          |
| `stage`     | the stage from `--stage`, read only; inside functions and resources, read it as `kdst.stage`     |
| `functions` | the functions, by name                                                                          |
| `resources` | any other aws resources, by name; functions and resources share their names                     |
| `http`      | the stage's http api: domain, cors, authorizers and throttling                                   |
| `outputs`   | values printed by `kdst outputs`                                                                 |
| `tags`      | tags added to everything koldstart creates, next to its own                                      |

## functions

```pkl
functions {
  ["api"] {
    runtime = "nodejs22.x"
    handler = "src/api.handler"
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
| `runtime`             |           | `nodejs22.x` or `nodejs24.x` for javascript and typescript, `provided.al2023` for go               |
| `handler`             |           | for node, the file and its export, `src/api.handler`; for go, the package to build, `./cmd/worker` |
| `memory`              | `1024`    | megabytes, from `128` to `10240`                                                                   |
| `timeout`             | `6.s`     | a pkl duration, at most `15.min`                                                                   |
| `architecture`        | `"arm64"` | `arm64` or `x86_64`                                                                                |
| `ephemeralStorage`    | `512`     | the size of `/tmp` in megabytes                                                                    |
| `reservedConcurrency` |           | the concurrency kept for this function alone                                                        |
| `logRetention`        | `30`      | days the log group keeps logs, one of the periods cloudwatch allows                                 |
| `environment`         |           | variables: strings, `kdst.ref()` values, secrets or outputs of other stages                         |
| `events`              |           | what invokes the function, see [events](#events)                                                    |
| `url`                 |           | the function's own url, see [function urls](#function-urls)                                         |
| `link`                |           | resources the function uses, see [link](#link)                                                      |
| `permissions`         |           | iam statements added to the function's role                                                         |
| `vpc`                 |           | `subnetIds` and `securityGroupIds` to put the function in                                           |
| `bundle`              |           | for node: `externals`, packages to leave out of the bundle, and `sourcemap`                         |
| `typecheck`           | `false`   | for typescript: run `tsc --noEmit` before bundling, which needs node and typescript installed       |

Each function gets a role and a log group of its own. The role may write to the log group, and has what `link`, `permissions`, its events and its runtime secrets add.

### building

Typescript and javascript are bundled with the esbuild that is built into koldstart, starting from the file in `handler`, so the zip holds one file and none of `node_modules`. Packages in `bundle.externals`, such as ones with native code, are left out of the bundle and copied from `node_modules`, with what they depend on; install them for the architecture the function runs on.

Go is built with `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and no build id, for the architecture of the function, into a binary named `bootstrap`, which is what the `provided.al2023` runtime runs.

Zips are deterministic: the same code gives the same bytes, on a laptop and in ci, so the hash of the zip tells koldstart whether a function's code changed. Go binaries also depend on the version of go that builds them, so pin it with a `toolchain` line in `go.mod`, such as `toolchain go1.27.3`; otherwise a laptop and ci with different versions upload different code.

### link

`link` lets a function use a resource of the project: its role gets a policy for the access, and the resource's name and arn become environment variables named after it.

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

### function urls

```pkl
url = new { auth = "iam" }
```

`auth` has no default: `"iam"` requires signed requests, `"none"` makes the url public. The url is the output `<function>Url`.

## events

Events are added to a function's `events`, using the classes in `kdst.pkl`.

```pkl
events {
  new kdst.HttpRoute { method = "GET"; path = "/orders/{id}" }
  new kdst.Schedule { expression = "rate(5 minutes)" }
  new kdst.Queue { queue = kdst.ref("jobs", "Arn"); batchSize = 10 }
  new kdst.Topic { topic = kdst.ref("alerts", "TopicArn") }
  new kdst.Stream { stream = kdst.ref("orders", "StreamArn"); startingPosition = "LATEST" }
  new kdst.Bucket { bucket = kdst.ref("uploads", "BucketName"); events { "s3:ObjectCreated:*" }; prefix = "incoming/" }
}
```

| class       | creates                                                                                            |
| ----------- | -------------------------------------------------------------------------------------------------- |
| `HttpRoute` | a route on the stage's http api, with its integration and permission; `authorizer` and `scopes` guard it |
| `Schedule`  | an eventbridge scheduler schedule, `rate(...)`, `cron(...)` or `at(...)`, with `timezone`, `input` and `enabled` |
| `Queue`     | an event source mapping from an sqs queue, with `batchSize`, `maximumBatchingWindow`, `reportBatchItemFailures` and `filters` |
| `Topic`     | an sns subscription and the permission for sns to invoke the function, with `filterPolicy`          |
| `Stream`    | an event source mapping from a kinesis stream or a dynamodb stream                                   |
| `Bucket`    | the function in the bucket's notifications, and the permission for s3 to invoke it                   |

A queue must hide a message for at least the function's timeout, its `VisibilityTimeout`, or lambda refuses the mapping; koldstart checks it for queues of the project. `reportBatchItemFailures` is on by default, so a function returns the messages that failed and only those are retried.

A bucket has one notification configuration, so koldstart puts the stage's functions in it and leaves every other notification as it found it. A bucket of the project that sets `NotificationConfiguration` itself cannot also have bucket events.

### the http api

All routes of a stage share one http api. Its url is the output `httpApiUrl`.

```pkl
http {
  domain = "api.example.com"
  certificate = "arn:aws:acm:eu-west-1:123456789012:certificate/..."
  cors = new { origins { "https://example.com" } }
  throttling = new { rate = 100; burst = 200 }
  authorizers {
    ["users"] = new kdst.JwtAuthorizer { issuer = "https://auth.example.com"; audience { "orders" } }
    ["keys"] = new kdst.LambdaAuthorizer { functionName = "authorize"; ttl = 5.min }
  }
}
```

With a domain, `httpApiUrl` is `https://<domain>`, and the output `httpDomainTarget` is where its dns record points. A route picks an authorizer with `authorizer = "users"`.

## resources

Anything that is not a function goes in `resources`. A resource has a CloudFormation type name and the properties CloudFormation documents for it, and koldstart creates, updates and deletes it through the [cloud control api](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/what-is-cloudcontrolapi.html), without a stack. The [resource type reference](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-template-resource-type-ref.html) lists the types and their properties. A type cloud control cannot create is refused before anything is planned.

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

Resources that hold data are retained: `kdst remove` leaves them in place, and the next deploy of the stage takes them over again. They are tables, buckets, databases, file systems, streams, keys, secrets, user pools, repositories, caches and search domains. `deletionPolicy` decides for one resource:

```pkl
["cache"] {
  type = "AWS::DynamoDB::Table"
  deletionPolicy = "delete"
}
```

A plan that would replace a retained resource stops, because the new one starts empty. `--allow-replace` replaces it, and leaves the old one in place; when the new one needs the old one's name, give it another.

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

The attributes of a resource are its properties as cloud control returns them, such as `Arn` and `TableName`. A function has `Arn` and `FunctionName`.

## outputs

```pkl
outputs {
  ["table"] = kdst.ref("orders", "TableName")
}
```

`kdst outputs` prints them, as does every deploy, with `httpApiUrl` and a `<function>Url` for each function url.

## stages

`--stage` picks the stage, and every stage is deployed separately, with its own resources, state and lock. A stage is lowercase letters, digits and dashes, at most 16, starting with a letter. `kdst.stage` is readable anywhere in the file, so one file describes all of them:

```pkl
memory = if (kdst.stage == "prod") 1024 else 256
```

A stage that differs a lot can have its own file that amends the main one, passed with `--config`:

```pkl
amends "koldstart.pkl"

region = "us-east-1"
```

One app reads another's outputs with `kdst.stageOutput(app, stage, output)`, from the state of that stage in the same account and region. A plan fails when that stage is not deployed, or has no such output.

## secrets

`kdst.ssm("/path")` is read from ssm parameter store, and `kdst.secretsmanager("name")` from secrets manager; `.field("key")` takes one key of a json secret. They are read while planning and deploying, so a plan shows when one changed, and their values become environment variables of the function. Their values never go into the state.

```pkl
environment {
  ["API_KEY"] = kdst.ssm("/orders/api-key")
  ["DB_USER"] = kdst.secretsmanager("orders-db").field("username")
  ["DB_PASSWORD"] = kdst.secretsmanager("orders-db").field("password").readAtRuntime()
}
```

The values are visible to anyone who can read the function's configuration. With `.readAtRuntime()`, the variable holds the arn of the parameter or secret instead, the function's role may read it, and the function reads it itself; with a `.field()`, `<NAME>_KEY` says which key, here `DB_PASSWORD_KEY=password`. A parameter encrypted with a kms key of your own also needs `kms:Decrypt` on it, through `permissions`.
