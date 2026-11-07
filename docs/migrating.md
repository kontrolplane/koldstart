# migrating from the serverless framework

`kdst migrate` reads a `serverless.yml` and writes a `koldstart.pkl` next to it. What it cannot convert, or converts to something that works differently, is listed at the end of the run by the `serverless.yml` key it came from, and, where it has a place in the file, left there as a `// TODO` comment on the line above it, with the yaml it is about after `// was:`. The list is grouped by what each needs, most urgent first: what to fix before you deploy, what was left out and is to write by hand, what works differently and is to check, and what koldstart does not need. When a TODO comment marks any of them, a line after the list says how many. The new file is evaluated with pkl before it is written, so it is valid pkl even where it is not complete, and what the checks of a deploy would refuse in it is listed under `koldstart.pkl` as a fix before you deploy.

```bash
kdst migrate
kdst migrate path/to/serverless.yml --out koldstart.pkl
```

Without a path, it reads `./serverless.yml`. It does not write over a `koldstart.pkl` that is there, unless you pass `--force`, and `--out`, or `-o`, writes it elsewhere. It ends with the `plan` to run next: with the `--stage` that `provider.stage` names, with `--config` when the file is not called `koldstart.pkl`, and after a `cd` into its directory when it is not this one:

```text
wrote PklProject, with the schema of koldstart 0.1.0
resolved it into PklProject.deps.json
wrote koldstart.pkl, with 6 things left to you

works differently, to check:
  provider.stage: deploy with --stage prod, or set KOLDSTART_STAGE=prod: koldstart deploys to dev by default
  provider.runtime: runs on nodejs22.x now, as koldstart does not build nodejs20.x: test it there
  provider.iam.role.statements: every function got these statements: keep in each what it needs, or use link

not needed in koldstart:
  custom: each ${self:custom...} got its value where it was used: tableName
  plugins.serverless-esbuild: koldstart bundles typescript with esbuild itself
  plugins.serverless-offline: kdst offline serves the http and rest routes on this machine

next:
  kdst plan --stage prod
```

The `koldstart.pkl` imports the schema as `@koldstart/`, and the `PklProject` beside it names the schema package of the koldstart you run, as `kdst init` writes it, resolved into `PklProject.deps.json`; commit both. A `PklProject` that is there already is kept, with the koldstart dependency added when it has none. When it cannot be resolved, offline for one, migrate says so and keeps the files, and the `plan` resolves it first. [configuration](./configuration.md#pklproject) has the rest.

koldstart does not read `serverless.yml` itself, and does not run serverless framework plugins. The conversion is done once, and the pkl file is what you keep.

## what is converted

| serverless.yml                                              | koldstart.pkl                                                        |
| ----------------------------------------------------------- | -------------------------------------------------------------------- |
| `service`                                                   | `app`, in lowercase                                                  |
| `provider.region`                                           | `region`                                                             |
| `provider.stage`                                            | a note to pass `--stage`, as koldstart deploys to `dev` by default    |
| `provider.runtime`, `memorySize`, `timeout`, `architecture`, `ephemeralStorageSize`, `logRetentionInDays`, `vpc` | set on every function that does not set its own |
| `provider.environment`                                      | merged into the environment of every function, the function's winning |
| `provider.tags`, `provider.stackTags`                       | `tags`, `provider.tags` winning                                      |
| `provider.tracing.lambda`, `functions.<name>.tracing`       | `tracing = true` on every function that traces                        |
| `provider.kmsKeyArn`, `functions.<name>.kmsKeyArn`          | `kmsKey`, the function's winning                                     |
| `provider.logs.httpApi`                                     | `http.accessLogs`, with its `format`                                 |
| `provider.logs.lambda`, `functions.<name>.logs`             | `logFormat`, `applicationLogLevel` and `systemLogLevel`, the function's winning |
| `provider.httpApi.cors`, `provider.httpApi.authorizers`     | `http.cors`, and `kdst.JwtAuthorizer` or `kdst.LambdaAuthorizer`      |
| `provider.iam.role.statements`, `provider.iamRoleStatements` | `permissions` on every function, with a note to narrow them; a function's own `iamRoleStatements` on it alone |
| `functions.<name>`                                          | a function of the same name, `_` and `.` as `-`                      |
| `functions.<name>.handler`                                  | `handler`; for go and rust, a placeholder directory to build, `./cmd/<name>`, with a note to set it |
| `provided`, `provided.al2`, `provided.al2023` and `go1.x` runtimes | `provided.al2023`, built as go, with a note to point `handler` at the go package or rust crate, or to set `bootstrap` to the binary your build writes; a note to test the older ones there; `package.artifact`, a zip koldstart does not take, with a note |
| `runtime: rust`, of the `serverless-rust` plugin             | `provided.al2023`, built with cargo lambda, with a note to set `handler` to the crate's directory, and to name its binary after the function when `handler` is `crate.bin` |
| `functions.<name>.url`                                      | `url = new { auth = "none" }`, `"iam"` with `authorizer: aws_iam`, and its `cors` |
| `functions.<name>.reservedConcurrency`, `ephemeralStorageSize` | `reservedConcurrency`, `ephemeralStorage`                        |
| `functions.<name>.provisionedConcurrency`                   | `provisionedConcurrency`, on the function's `live` alias, which its events invoke |
| `functions.<name>.snapStart`                                | `snapStart`, for python functions                                    |
| `functions.<name>.tags`                                     | `tags` of the function                                               |
| `functions.<name>.onError`                                  | `deadLetter`                                                         |
| `functions.<name>.destinations`, `maximumRetryAttempts`, `maximumEventAge` | `async`; a destination that names a function is its `kdst.ref()` |
| `functions.<name>.fileSystemConfig`                         | `fileSystem = new { accessPoint; mountPath }`                         |
| python runtimes, and a handler such as `src.handler.main`   | `python3.12` or `python3.13`, older ones as `python3.12`, and `src/handler.main` |
| `functions.<name>.layers`                                   | `layers`, by their arns                                              |
| `functions.<name>.image` of `provider.ecr.images`           | `image { context; dockerfile; buildArgs; command; entryPoint; workingDirectory }` |
| `events: httpApi`                                           | `kdst.get("/path")` and the other methods' helpers, `*` as `kdst.any("$default")`, amended with its authorizer and scopes, and `iam = true` for `aws_iam` |
| `events: http`                                              | `kdst.rest("GET", "/path")`, a route of the rest api, amended with `private`, the `request.parameters` a request must have as `requiredQuery` and `requiredHeaders`, `timeoutInMillis`, its own or `provider.apiGateway`'s, as `timeout`, and its `authorizer` in `rest.authorizers`: `iam = true` for `aws_iam`, a `kdst.TokenAuthorizer` or `kdst.RequestAuthorizer` of a function of the service, with its identity sources, validation expression and ttl, 300 seconds by default, or a `kdst.CognitoAuthorizer` of a user pool's arn or one of `resources`, with its `scopes` |
| `events: schedule`                                          | `kdst.schedule("rate(...)")`, one for each rate, amended with `enabled`, `timezone` and `input` |
| `events: sqs`                                               | `kdst.fromQueue()`, of a queue of `resources` by its name or of an arn; `kdst.Queue` for any other value; with `maximumConcurrency` and `enabled` |
| `events: stream`                                            | `kdst.fromStream()` or `kdst.fromTableStream()`, the same way; `kdst.Stream` for any other value; with its retries, record age, bisecting, parallelization, tumbling window, `destinations.onFailure` and `enabled` |
| `events: sns`                                               | `kdst.fromTopic()`, the same way, with its `filterPolicy`; a topic given by name becomes a resource, as the serverless framework made it |
| `events: s3`                                                | `kdst.fromBucket()` of a bucket that is not `existing`, which becomes a resource; `kdst.Bucket` of one that is; with its `event` and the `prefix` and `suffix` of its `rules` |
| `events: eventBridge` with a `pattern`                      | `kdst.EventRule`, with `eventBus`, `input`, `inputPath`, `inputTransformer`, `retryPolicy`, `deadLetterQueueArn` and `enabled` |
| `events: websocket`, and `websocketsApiRouteSelectionExpression` | `kdst.fromWebSocket()`, and `websocket.routeSelection`         |
| `events: alb`                                               | `kdst.LoadBalancer`, with its path, host, method, header, query and ip conditions |
| `events: cloudwatchLog`                                     | `kdst.fromLogs()`, amended with its `filter`                          |
| `resources.Resources`                                       | `resources`, the same type and properties, and `DeletionPolicy`, `Snapshot` as `retain` |
| a table, queue, bucket or topic with what its shorthand sets | `kdst.table()`, `kdst.queue()`, `kdst.bucket()` or `kdst.topic()`, amended with the other properties; written out in full when any of what the shorthand sets differs |
| `resources.Outputs`                                         | `outputs`                                                            |
| `Ref`, `!Ref`                                               | `kdst.ref(<name>, "Identifier")`, or `"FunctionName"` for a function  |
| `Fn::GetAtt`, `!GetAtt`                                     | `kdst.ref(<name>, <attribute>)`                                      |
| `Fn::Sub`, with its variables or without                     | a pkl string, or `kdst.join(List(...))` when a ref or the account is in it |
| `Fn::Join`                                                  | the same                                                             |
| `AWS::Region`, `AWS::AccountId`, `AWS::Partition`, `AWS::URLSuffix`, `AWS::StackName` | `module.region`, `kdst.accountId`, `kdst.partition`, `kdst.urlSuffix`, `"\(module.app)-\(kdst.stage)"` |
| `Conditions` of `Fn::Equals`, `Fn::Not`, `Fn::And`, `Fn::Or` | a `local` boolean each, such as `kdst.stage == "prod"`               |
| `Fn::If`, and `AWS::NoValue`                                | `if (...) a else b`, or `when (...) { ... }` when a side is left out |
| `Condition` of a resource or an output                      | `when (...) { ... }` around it                                       |
| `Mappings` and `Fn::FindInMap`                              | a `local` Mapping, looked up by its keys                             |
| `Fn::Base64` of a string, `Fn::Select` of a list written out | `.base64`, the item                                                 |
| `<Function>LambdaFunction`, the logical id of a function    | that function                                                        |
| `${ssm:/${self:service}/${sls:stage}/<name>}`               | `kdst.param("<name>")`                                               |
| `${ssm:/aws/reference/secretsmanager/${self:service}/${sls:stage}/<name>}` | `kdst.secret("<name>")`                                |
| `${ssm:/path}`, any other path                              | `kdst.ssm("/path")`, its variables interpolated                      |
| `${cf:<service>-<stage>.<Output>}`                          | `kdst.stageOutput("<service>", "<stage>", "<Output>")`               |
| `${cf(<region>):...}`, `${cf.<region>:...}`                 | the same, amended with `{ region = "<region>" }`                     |
| `${env:NAME}`, `${env:NAME, 'default'}`                     | `read("env:NAME")`, `read?("env:NAME") ?? "default"`                 |
| `${file(./x.yml):key}`, `${file(./x.json)}`                 | the value, or the object or list, read from the file next to `serverless.yml` |
| `params`, `stages.<stage>.params`, and `${param:name}`      | a `local` Mapping by stage, and `param("name")`, the stage's or default's |
| `${aws:accountId}`                                          | `kdst.accountId`                                                     |
| `${sls:stage}`, `${opt:stage}`, `${self:provider.stage}`    | `kdst.stage`                                                         |
| `${self:service}`, `${self:provider.region}`, `${aws:region}`, `${opt:region}` | `module.app`, `module.region`                     |
| `${self:...}` of anything else, such as `custom`            | the value it names                                                   |

A variable inside a longer string becomes pkl interpolation: `orders-${sls:stage}` is `"orders-\(kdst.stage)"`. An ssm path is the app's and stage's when it starts with the service, by name or as `${self:service}`, then the stage, as `${sls:stage}` or `${opt:stage}`: `${ssm:/orders/${sls:stage}/api-key}` is `kdst.param("api-key")`, and `${ssm:/shared/${sls:stage}/key}` stays `kdst.ssm("/shared/\(kdst.stage)/key")`.

## what is left to you

- the `authorizer` of an `events: http` route that is not converted, one by its `authorizerId` or a function of another service: a fix before you deploy, as the route is open without it; so is a token authorizer that reads its token from anything but a header. A `private` route takes the api key koldstart makes, not the serverless framework's: hand callers its value, of the key the output `restApiKeyId` names. A route of `aws_iam`, of either api, is on an api koldstart makes anew, so the policies of its callers need its id for `execute-api:Invoke`. A required parameter gets a note, as api gateway refuses a request without it, and an optional one changes nothing for a proxy integration.
- what koldstart's rest api does not do yet: `request.schemas`, `cors`, which a function answers with a route of `"OPTIONS"`, `async`, `caching` and `connectionType`; `integration: lambda`, which becomes the proxy integration with a note, and an `http`, `http-proxy` or `mock` integration, which leaves the route out. Of `provider.apiGateway`, `timeoutInMillis` becomes each route's `timeout`, `apiKeys` and `usagePlan` get a note, and the rest is left. The rest api is regional, where the serverless framework's was edge-optimised by default.
- `provider.iam`: the statements become `permissions` of every function, with a note to narrow them to what each function needs, or to replace them with `link`. A role of your own, `provider.iam.role` or a function's `role`, is not taken over: every function gets one of its own. Statements with a `Condition`, `NotAction`, `NotResource` or `Principal` are left out.
- `Fn::Select` of a list only known while deploying, such as `Fn::GetAZs`, and `Fn::Split`, `Fn::ImportValue`, `Fn::Cidr` and `Fn::Transform`, which become pkl expressions or values of your own; `AWS::StackId` and `AWS::NotificationARNs`.
- a condition on a value only known while deploying, such as a ref.
- `${file()}` of anything but yaml or json, such as javascript, which runs code, and the other variable sources, which become an `import`, a `read()` or a value of your own; a secret or an output inside a longer string.
- `custom`, and `plugins`, which do not run in koldstart. What refers to `custom` gets its value. Some plugins are no longer needed: koldstart bundles with esbuild itself, for `serverless-esbuild`, `serverless-plugin-typescript` and `serverless-webpack`; installs `requirements.txt` or `pyproject.toml` with uv, for `serverless-python-requirements`; builds rust with cargo lambda, for `serverless-rust`, whose `custom.rust` flags go in each crate's `Cargo.toml`; gives every function a role of its own, for `serverless-iam-roles-per-function`; and keeps 3 versions of a function with an alias, for `serverless-prune-plugin`. `serverless-offline` is `kdst offline`. For `serverless-domain-manager` set `http.domain` and `http.certificate`, for `serverless-plugin-log-retention` set `logRetention`, and for `serverless-dotenv-plugin` and `useDotenv`, as koldstart reads no `.env`, export the variables and read them with `read("env:NAME")`. Any other plugin, or one of your own by its path, is for you to do another way.
- runtimes other than node, go, python and rust, which koldstart does not build, such as java, .NET and ruby: those functions are left out, to build as a `bootstrap` or a container image; and a function of a prebuilt image, given by its uri, which koldstart does not take, as it builds images itself.
- `layers` that `serverless.yml` builds, which koldstart does not build: publish them, and give functions their arns. A go or rust function's layers, which they do not take.
- `snapStart` of a function that is not python, which lambda takes for java and .NET as well, runtimes koldstart does not build. `provider.versionFunctions`: koldstart publishes versions only of functions with `provisionedConcurrency` or `snapStart`. `provisionedConcurrency` is on the function's `live` alias: call the function as `<name>:live` to use it.
- `provider.logs.restApi` and `provider.logs.websocket`: the rest api and the websocket api have no access logs yet. `provider.tracing.apiGateway`: koldstart's apis have no x-ray tracing.
- a kinesis stream's `consumer` and `startingPositionTimestamp`.
- the policy of an `eventBridge` event's `deadLetterQueueArn`, which must let eventbridge send to it.
- events other than the ones above, such as `cognitoUserPool`, `iot`, `msk` and `kafka`; an `eventBridge` event of a schedule, which is `kdst.schedule()`; a websocket route's `authorizer` and `routeResponseSelectionExpression`.
- resources of a type that is not `AWS::`, such as `Custom::`, which cloud control does not create. Of a resource's attributes, `CreationPolicy` and `UpdatePolicy` are left out and `Metadata` dropped; `DependsOn` gets a note, as koldstart orders resources by their refs. An output's `Export` gets a note too: another app reads it with `kdst.stageOutput()`.
- a function's `name`, as koldstart names it `<app>-<stage>-<function>`, and a function renamed for its `_` or `.`: update what calls it by name. `disableLogs`: every function gets a log group. `logs.logGroup`: each function logs to `/aws/lambda/<name>`.
- `provider.deploymentBucket`, as koldstart keeps its zips and state in a bucket of its own, and `stepFunctions`, to declare in `resources` as `AWS::StepFunctions::StateMachine`.

## moving a stage that is already deployed

A stage deployed by the serverless framework lives in a CloudFormation stack, which koldstart does not take over. The way across is to deploy the converted project as a new stage next to it, move the traffic, and remove the old stack with `serverless remove`.

Resources that hold data, such as tables and buckets, should not be recreated. Give them `DeletionPolicy: Retain` in `serverless.yml` before removing the stack, so they survive it, then take each one over with `kdst import`:

```bash
kdst import OrdersTable orders-prod --stage prod
```
