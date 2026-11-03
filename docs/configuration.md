# configuration

> [!NOTE]
> This describes the design in [#1](https://github.com/kontrolplane/koldstart/issues/1). Nothing here is implemented yet, and names can still change before `0.1.0`.

A koldstart project is one [pkl](https://pkl-lang.org/) file, `koldstart.pkl` by default, that amends the `Project.pkl` module koldstart publishes. Amending it gives you type checking, defaults and completion in any editor with a pkl plugin, and a mistake such as a misspelled property or a memory size lambda does not offer is reported when the file is evaluated, before anything is built.

```pkl
amends "package://github.com/kontrolplane/koldstart/releases/download/koldstart@0.1.0/koldstart@0.1.0#/Project.pkl"

import "package://github.com/kontrolplane/koldstart/releases/download/koldstart@0.1.0/koldstart@0.1.0#/Events.pkl"

app = "orders"
region = "eu-west-1"
```

The package version follows the koldstart version. A file written against one schema version keeps evaluating on later releases of the same major version.

## project

| property    | description                                                                          |
| ----------- | ------------------------------------------------------------------------------------ |
| `app`       | name of the application, used in the name of everything deployed and in its state key |
| `region`    | aws region to deploy into, `--region` takes precedence                               |
| `stage`     | the stage from `--stage`, read only, use it to vary values per stage                  |
| `functions` | the functions, by name                                                               |
| `resources` | any other aws resources, by name                                                     |
| `outputs`   | values printed by `kdst outputs` and shown in `kdst ui`                               |
| `tags`      | tags added to everything koldstart creates, next to its own                          |

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

| property       | description                                                              |
| -------------- | ------------------------------------------------------------------------ |
| `runtime`      | `nodejs20.x`, `nodejs22.x`, or `provided.al2023` for go                  |
| `handler`      | for node, the file and export, `src/api.handler`; for go, the package to build, `./cmd/worker` |
| `memory`       | memory in megabytes, between `128` and `10240`                            |
| `timeout`      | a pkl duration, at most `15.min`                                          |
| `architecture` | `arm64` or `x86_64`                                                       |
| `environment`  | environment variables, plain strings, `ref()` values or secret references |
| `events`       | what invokes the function, see [events](#events)                          |
| `url`          | `true` to give the function its own function url                          |

### building

Typescript and javascript are bundled with the esbuild that is built into koldstart, starting from the file in `handler`, so the zip holds one bundled file and none of `node_modules`. When `tsc` is installed, typescript is type checked before it is bundled.

Go is built with `CGO_ENABLED=0 go build -tags lambda.norpc` for the architecture of the function, into a binary named `bootstrap`, which is what the `provided.al2023` runtime runs.

Zips are deterministic: the same files give the same bytes, so the hash of the zip tells koldstart whether a function's code changed.

## events

Events are added to a function's `events` listing, using the classes in `Events.pkl`.

```pkl
events {
  new Events.HttpRoute { method = "GET"; path = "/orders/{id}" }
  new Events.Schedule { expression = "rate(5 minutes)" }
  new Events.Queue { queue = ref("jobs", "Arn"); batchSize = 10 }
  new Events.Topic { topic = ref("alerts", "TopicArn") }
  new Events.Stream { stream = ref("orders", "StreamArn"); startingPosition = "LATEST" }
  new Events.Bucket { bucket = ref("uploads", "BucketName"); events { "s3:ObjectCreated:*" }; prefix = "incoming/" }
}
```

| class       | creates                                                                       |
| ----------- | ----------------------------------------------------------------------------- |
| `HttpRoute` | a route on the stage's api gateway http api, with its integration and permission |
| `Schedule`  | an eventbridge scheduler schedule, `rate(...)` or `cron(...)`                  |
| `Queue`     | an event source mapping from an sqs queue                                     |
| `Topic`     | an sns subscription and the permission for sns to invoke the function          |
| `Stream`    | an event source mapping from a kinesis stream or dynamodb stream               |
| `Bucket`    | an s3 bucket notification and the permission for s3 to invoke the function     |

All http routes of a stage share one http api. Its url is the `httpApiUrl` output.

## resources

Anything that is not a function goes in `resources`. A resource has a CloudFormation type name and the properties CloudFormation documents for it, and koldstart creates, updates and deletes it through the [cloud control api](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/what-is-cloudcontrolapi.html), without a stack. The [resource type reference](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-template-resource-type-ref.html) lists the types and their properties. A type cloud control does not support is reported when the file is evaluated.

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

## references

`ref(name, attribute)` stands for an attribute of a resource that is only known once it exists, such as an arn or a generated name. koldstart resolves it while applying, and orders the work so the resource exists before anything that refers to it.

```pkl
environment {
  ["BUCKET"] = ref("uploads", "BucketName")
}
```

The attributes are the ones CloudFormation's `Fn::GetAtt` returns for that type. Functions can be referred to as well: `ref("api", "Arn")`.

## stages

`--stage` picks the stage, and every stage is deployed separately, with its own resources, state and lock. `stage` is readable in the file, so one file describes all of them:

```pkl
memory = if (stage == "prod") 1024 else 256
```

A stage that differs a lot can have its own file that amends the main one, passed with `--config`:

```pkl
// koldstart.prod.pkl
amends "koldstart.pkl"

region = "us-east-1"
```

## secrets

A string in the form `${ssm:/path}` is read from ssm parameter store, and `${secretsmanager:name}` or `${secretsmanager:name:key}` from secrets manager, the latter taking one key from a json secret. They are resolved during a deploy, and their values become environment variables of the function.

The resolved values are visible to anyone who can read the function's configuration. To keep them out of it, set `secretsAtRuntime = true` on the function: the environment variable then holds the arn of the parameter or secret, the function is allowed to read it, and the function reads it itself when it starts.
