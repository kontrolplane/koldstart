# migrating from the serverless framework

`kdst migrate` reads a `serverless.yml` and writes a `koldstart.pkl` next to it. What it cannot convert is left in the file as a `// TODO` comment, on the line above where it belongs, and listed at the end of the run. The new file is checked with pkl before it is written, so it is valid pkl, and passes the checks a deploy makes, even where it is not complete.

```bash
kdst migrate
kdst migrate path/to/serverless.yml --out koldstart.pkl
```

Without a path, it reads `./serverless.yml`. It does not write over a `koldstart.pkl` that is there, unless you pass `--force`.

koldstart does not read `serverless.yml` itself, and does not run serverless framework plugins. The conversion is done once, and the pkl file is what you keep.

## what is converted

| serverless.yml                                              | koldstart.pkl                                                        |
| ----------------------------------------------------------- | -------------------------------------------------------------------- |
| `service`                                                   | `app`, in lowercase                                                  |
| `provider.region`                                           | `region`                                                             |
| `provider.stage`                                            | a note to pass `--stage`, as koldstart deploys to `dev` by default    |
| `provider.runtime`, `memorySize`, `timeout`, `architecture`, `logRetentionInDays`, `vpc` | set on every function that does not set its own |
| `provider.environment`                                      | merged into the environment of every function, the function's winning |
| `provider.tags`                                             | `tags`                                                               |
| `provider.httpApi.cors`, `provider.httpApi.authorizers`     | `http.cors`, and `kdst.JwtAuthorizer` or `kdst.LambdaAuthorizer`      |
| `provider.iam.role.statements`, `iamRoleStatements`         | `permissions` on every function, with a note to narrow them           |
| `functions.<name>`                                          | a function of the same name, `_` and `.` as `-`                      |
| `functions.<name>.handler`                                  | `handler`; for go, a placeholder package to build                    |
| `functions.<name>.url`                                      | `url = new { auth = "none" }`, `"iam"` with `authorizer: aws_iam`, and its `cors` |
| `functions.<name>.reservedConcurrency`, `ephemeralStorageSize` | `reservedConcurrency`, `ephemeralStorage`                        |
| `events: httpApi`                                           | `kdst.HttpRoute`, `*` as `$default`, with its authorizer and scopes   |
| `events: http`                                              | `kdst.HttpRoute` on the http api, with a note                         |
| `events: schedule`                                          | `kdst.Schedule`, one for each rate, with `enabled` and `input`        |
| `events: sqs`                                               | `kdst.Queue`                                                         |
| `events: stream`                                            | `kdst.Stream`                                                        |
| `events: sns`                                               | `kdst.Topic`; a topic given by name becomes a resource, as the serverless framework made it |
| `events: s3`                                                | `kdst.Bucket`; a bucket that is not `existing` becomes a resource     |
| `resources.Resources`                                       | `resources`, the same type and properties, and `DeletionPolicy`       |
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
| `${ssm:/path}`                                              | `kdst.ssm("/path")`                                                  |
| `${cf:<service>-<stage>.<Output>}`                          | `kdst.stageOutput("<service>", "<stage>", "<Output>")`               |
| `${env:NAME}`, `${env:NAME, 'default'}`                     | `read("env:NAME")`, `read?("env:NAME") ?? "default"`                 |
| `${file(./x.yml):key}`, `${file(./x.json)}`                 | the value, or the object or list, read from the file next to `serverless.yml` |
| `params`, `stages.<stage>.params`, and `${param:name}`      | a `local` Mapping by stage, and `param("name")`, the stage's or default's |
| `${aws:accountId}`                                          | `kdst.accountId`                                                     |
| `${sls:stage}`, `${opt:stage}`                              | `kdst.stage`                                                         |
| `${self:service}`, `${self:provider.region}`, `${aws:region}` | `module.app`, `module.region`                                      |
| `${self:...}` of anything else, such as `custom`            | the value it names                                                   |

A variable inside a longer string becomes pkl interpolation: `orders-${sls:stage}` is `"orders-\(kdst.stage)"`.

## what is left to you

- `events: http` and `provider.apiGateway`, the rest api of api gateway. koldstart deploys http apis only. The route is converted to an `HttpRoute`, with a note, because request validation, usage plans and api keys have no equivalent.
- `provider.iam`: the statements become `permissions` of every function, with a note to narrow them to what each function needs, or to replace them with `link`. A role of your own is not taken over. Statements with a `Condition`, `NotAction`, `NotResource` or `Principal` are left out.
- `Fn::Select` of a list only known while deploying, such as `Fn::GetAZs`, and `Fn::Split`, `Fn::ImportValue`, `Fn::Cidr` and `Fn::Transform`, which become pkl expressions or values of your own; `AWS::StackId` and `AWS::NotificationARNs`.
- a condition on a value only known while deploying, such as a ref.
- `${file()}` of javascript or typescript, which runs code, and the other variable sources, which become an `import`, a `read()` or a value of your own; a secret or an output inside a longer string.
- `custom` and `plugins`. What refers to `custom` gets its value. Some plugins are no longer needed: koldstart bundles typescript itself, so `serverless-esbuild` and `serverless-plugin-typescript` can go, `serverless-offline` is `kdst offline`, and every function has its own role already.
- runtimes other than node and go, `layers` and container image functions, which koldstart does not deploy yet: those functions are left out.
- events other than the ones above, such as `websocket`, `eventBridge` and `alb`.
- resources of a `Custom::` type, and `DependsOn`, `Condition` and the other attributes of a resource besides `DeletionPolicy`.

## moving a stage that is already deployed

A stage deployed by the serverless framework lives in a CloudFormation stack, which koldstart does not take over. The way across is to deploy the converted project as a new stage next to it, move the traffic, and remove the old stack with `serverless remove`.

Resources that hold data, such as tables and buckets, should not be recreated. Give them `DeletionPolicy: Retain` in `serverless.yml` before removing the stack, so they survive it, then take each one over with `kdst import`:

```bash
kdst import OrdersTable orders-prod
```
