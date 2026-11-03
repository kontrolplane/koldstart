# migrating from the serverless framework

> [!NOTE]
> This describes the design in [#1](https://github.com/kontrolplane/koldstart/issues/1). Nothing here is implemented yet.

`kdst migrate` reads a `serverless.yml` and writes a `koldstart.pkl` next to it. What it cannot translate is left in the file as a `// TODO` comment at the place it belongs, and listed in a summary at the end of the run. The new file is evaluated before it is written, so it is valid pkl even when it is not complete.

```bash
kdst migrate                      # ./serverless.yml
kdst migrate path/to/serverless.yml
```

koldstart does not read `serverless.yml` itself, and does not run serverless framework plugins. The conversion is done once, and the pkl file is what you keep.

## what is converted

| serverless.yml                                      | koldstart.pkl                                         |
| --------------------------------------------------- | ----------------------------------------------------- |
| `service`                                           | `app`                                                 |
| `provider.region`                                   | `region`                                              |
| `provider.stage`                                    | the default for `--stage`, noted in a comment         |
| `provider.runtime`, `memorySize`, `timeout`, `architecture` | copied to every function that does not set its own |
| `provider.environment`                              | merged into the environment of every function         |
| `provider.tags`                                     | `tags`                                                |
| `functions.<name>.handler`                          | `handler`                                             |
| `functions.<name>.url`                              | `url = true`                                          |
| `events: httpApi`                                   | `Events.HttpRoute`                                    |
| `events: schedule`                                  | `Events.Schedule`                                     |
| `events: sqs`                                       | `Events.Queue`                                        |
| `events: sns`                                       | `Events.Topic`                                        |
| `events: stream`                                    | `Events.Stream`                                       |
| `events: s3`                                        | `Events.Bucket`                                       |
| `resources.Resources`                               | `resources`, the same type and properties             |
| `resources.Outputs`                                 | `outputs`                                             |
| `Ref`, `Fn::GetAtt`                                 | `ref()`                                               |
| `${ssm:...}`                                        | kept as is                                            |
| `${env:NAME}`                                       | `read("env:NAME")`                                    |
| `${opt:stage}`, `${sls:stage}`                      | `stage`                                               |
| `${self:...}`                                       | a reference to the value in the pkl file              |

## what is left to you

- `events: http`, the rest api of api gateway. koldstart deploys http apis only. The route is converted to an `HttpRoute`, with a note, because request validation, usage plans and api keys have no equivalent.
- `provider.iam`, roles and statements. They are copied to the project with a note, to be checked against what each function needs.
- `Fn::Sub`, `Fn::Join`, `Fn::If` and the other intrinsic functions besides `Ref` and `Fn::GetAtt`, which become pkl expressions by hand.
- `${file(...)}`, `${cf:...}` and other variable sources, which become an `import`, a `read()` or a `ref()`.
- `custom` and `plugins`. Some plugins are no longer needed: koldstart bundles typescript itself, so `serverless-esbuild` and `serverless-plugin-typescript` can go, and `serverless-offline` is `kdst offline`.
- `layers` and container image functions, which koldstart does not deploy yet.

## moving a stage that is already deployed

A stage deployed by the serverless framework lives in a CloudFormation stack, which koldstart does not take over. The way across is to deploy the converted project as a new stage next to it, move the traffic, and remove the old stack with `serverless remove`.

Resources that hold data, such as tables and buckets, should not be recreated. Give them `DeletionPolicy: Retain` in `serverless.yml` before removing the stack, so they survive it. koldstart cannot take over a resource it did not create yet, so for now a retained resource is referred to by its name or arn as a plain string, rather than declared in `resources`. Importing existing resources is an open question in [#1](https://github.com/kontrolplane/koldstart/issues/1).
