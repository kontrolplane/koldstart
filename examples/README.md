# examples

Three kinds, each a project of its own with a README of what it shows and how to try it:

- [concepts](#concepts): one thing koldstart does, in a `koldstart.pkl` that fits on a screen and a handler of a few lines with nothing to install.
- [runtimes](#runtimes): how koldstart builds, packages and runs one language, written as a team would in production, with its tests.
- [reference-application](./reference-application): the concepts and runtimes together, in one application set up as a team would run it in production, with a runbook for its alarms. The [end-to-end tests](../test/e2e) deploy it.

## concepts

| concept                                             | what it shows                                                                        |
| --------------------------------------------------- | ------------------------------------------------------------------------------------ |
| [stages](./concepts/stages)                         | one file for development, staging and production, and what differs between them       |
| [defaults](./concepts/defaults)                     | what every function gets unless it sets its own: memory, timeout, logs and tracing    |
| [tags](./concepts/tags)                             | tags on everything a stage creates, and finding a stage's resources by them           |
| [outputs](./concepts/outputs)                       | the values of a stage people and other apps need, and the ones koldstart adds         |
| [stage-outputs](./concepts/stage-outputs)           | one app reading another's output from its state, with `kdst.stageOutput()`            |
| [link](./concepts/link)                             | a function using a resource of its project: the access, and the variables it is given |
| [permissions](./concepts/permissions)               | statements for what link does not cover, scoped with `kdst.join()` and `kdst.ref()`   |
| [secrets](./concepts/secrets)                       | values from parameter store and secrets manager, read while deploying or at runtime   |
| [resources](./concepts/resources)                   | any other aws type through cloud control, the shorthands, and what is kept on remove  |
| [encryption-keys](./concepts/encryption-keys)       | a kms key of the stage's own, for a table and a function's environment variables      |
| [http-routes](./concepts/http-routes)               | the stage's http api: routes, path parameters, request limits, access logs and cors   |
| [signed-routes](./concepts/signed-routes)           | routes for callers with aws credentials alone, signed with signature version 4        |
| [function-urls](./concepts/function-urls)           | an https endpoint of one function, public or for signed requests                      |
| [websockets](./concepts/websockets)                 | the stage's websocket api, its connections, and posting back to them                  |
| [schedules](./concepts/schedules)                   | cron, rate and one-off schedules, in a timezone, on the stages that want them         |
| [queues](./concepts/queues)                         | a producer and a worker, batches, failures of single messages, storing each one once  |
| [dead-letter-queues](./concepts/dead-letter-queues) | where what failed every time goes, for each way a function is handed its events       |
| [alarms](./concepts/alarms)                         | an alarm for every function, a heartbeat for a schedule, and paging on production     |

## runtimes

| runtime                                       | example                                | shows                                                                      |
| --------------------------------------------- | -------------------------------------- | -------------------------------------------------------------------------- |
| [typescript](./runtimes/typescript)           | attachments, through urls to s3        | bundling with esbuild, npm packages, type checking, source maps            |
| [go](./runtimes/go)                           | feature flags, changed with `if-match` | cross-compiling to a `bootstrap`, a pinned toolchain                       |
| [python](./runtimes/python)                   | shipping quotes, checked with pydantic | dependencies with native code, installed by uv for lambda from any machine |
| [rust](./runtimes/rust)                       | shipping quotes, from published rates  | a crate built with cargo lambda                                            |
| [container-image](./runtimes/container-image) | a status endpoint                      | a dockerfile, an image stamped by continuous integration, ecr              |
| [bootstrap](./runtimes/bootstrap)             | a clock in sh                          | an executable built elsewhere, which is its own runtime                    |

## trying one

Every example's `PklProject` names the schema package of koldstart 0.1.0, which its `koldstart.pkl` imports as `@koldstart/`. Each README's commands pass `--stage`: without it, or `KOLDSTART_STAGE`, koldstart picks a stage name of its own, which the examples size as development.

`make test-examples` runs the unit tests of the reference application and of every runtime, as continuous integration does. [examples_test.go](./examples_test.go) evaluates every example against the schema in this tree, on development, staging and production and on the stage koldstart picks without `--stage`, and checks a deploy would accept it.
