# how it works

A deploy goes through the same steps every time. The configuration is evaluated, the functions are built, the result is compared with what the state says was deployed, and the difference is applied with direct calls to the aws apis.

```text
koldstart.pkl ──► pkl eval ──► build + zip ──► desired graph
                                                    │
                     s3: <app>/<stage>/state.json ──┤ compare
                                                    ▼
                                                  plan ──► kdst plan prints it
                                                    │
                                                    ▼ kdst deploy
                                  apply, in dependency order
                                  ├── lambda, iam, logs, api gateway, scheduler, sns, s3
                                  └── cloud control for everything in resources
                                                    │
                                                    ▼
                                  state written before and after every step
```

## planning

The resource types are looked up in the cloudformation registry first: a type cloud control cannot create, or properties its schema does not allow, stop the plan before anything else is read. The configuration then becomes a graph. Every function, event and resource is a node, together with the role, log group and permissions that come with it. Every `kdst.ref()` and every implicit dependency, such as a route needing its api, is an edge. Each node's inputs are resolved as far as they can be, secrets and outputs of other stages read, and the result is compared with the node's entry in the state:

- `+`, create: in the configuration, not in the state
- `~`, update: in both, and the inputs differ in a way aws can change in place
- `-/+`, replace: in both, but a property changed that aws does not allow to be changed in place, such as a table's key schema; for resources, the type's schema in the cloudformation registry says which
- `-`, delete: in the state, no longer in the configuration
- `=`, leave in place: no longer in the configuration, and kept, because it holds data or was imported
- `<`, take over again: left in place by an earlier remove, and managed again
- no change, which the plan leaves out

The state keeps the inputs every node was last applied with, so a plan shows what changes under each update, `memory: 256 → 1024`, and a resource is updated with a patch of only that. A value that refers to something not created yet is shown as known after apply, and a secret as `(a secret)`.

`kdst plan` prints this list, `plan for orders/dev in eu-west-1, account 123456789012` above it and the counts below, `2 to create, 1 to update`, and changes nothing in aws or the state. A node a deploy that failed left half done is marked `(finishing what a deploy started)`. With nothing to do, it says `no changes: what the state says is deployed matches the configuration`. It does not read the live resources, so a change made by hand in the console is not part of the plan.

`kdst plan --refresh` does read them. It reads every resource of the state as it is in aws. One that is gone, because something outside koldstart deleted it, is created again, marked `(deleted outside koldstart)`, and what refers to it is updated. Some types say which inputs were changed outside koldstart: functions, through lambda, and resources, through cloud control, where what aws adds and the order of lists don't count. Such a resource is updated from how it is now, so the change is set back, under a line such as `changed outside koldstart: memory, timeout`. For the other types, the plan only says whether they are still there. With nothing to do, it says `no changes: what is in aws matches the configuration`. `kdst deploy --refresh` applies such a plan, and the ui's `r` on a plan makes one.

## applying

`kdst deploy` prints the plan, asks, `apply this plan? [y/N]`, or goes on with `--yes`, and applies it in dependency order, eight nodes at a time where they do not depend on each other. A plan with no changes is not asked about. Deletes come last and go the other way, so nothing is removed while something still points at it. A plan made against a state that has changed since is refused.

When the only difference is the code of one or more functions, the plan ends with `only code changes: the functions' code is replaced in place`, and the deploy uploads the new zips and makes one `UpdateFunctionCode` call each. `kdst deploy function -f <fn>` always takes this path, for one function, without asking, and refuses when that function's configuration changed too; when its code is the code deployed, it says it is up to date.

Each node gets a line on stderr as it finishes, `✓ function/api  updated in 1.1s`, or `✗` and how long it ran when it failed or was stopped. The deploy ends with what it did on stdout, `deployed orders/dev in 9.8s: 7 created`, and the outputs; one that failed or was stopped says `failed after` or `stopped after` on stderr instead, and the error says to deploy again to finish. On a terminal, a status line under them shows how many nodes are done, which are running and for how long. Without one, as in ci, the lines are plain and a node still running after 30 seconds says so every 30 seconds, `… function/api  still updating after 30s`. Colour is off when the output is not a terminal, `NO_COLOR` is set or `TERM` is `dumb`.

## state

The state lives in a bucket in your account, `koldstart-state-<account>-<region>`, which the first deploy creates with versioning and encryption turned on and public access blocked. Every call to it names your account as the bucket's owner, so a bucket of the same name in another account is refused rather than written to.

| key                                | holds                                                    |
| ---------------------------------- | -------------------------------------------------------- |
| `<app>/<stage>/state.json`         | every resource of the stage: its type, its id in aws, the inputs it was applied with, a hash of them, and its attributes |
| `<app>/<stage>/lock`               | who holds the stage's lock, from where, and since when    |
| `zips/<app>/<stage>/<sha256>.zip`  | the functions' code, by its hash                          |

Secrets and outputs of other stages are kept in the state as references, never as values. A hash of their values tells the next deploy whether they changed.

The bucket keeps the last 100 versions of each state, so earlier deploys can be looked back at, and expires zips after 90 days: lambda copies the code when it is deployed, and a deploy of the same code uploads it again when it is gone.

## locking

A deploy, `deploy function`, remove or import, and a deploy or remove from the ui, first writes `<app>/<stage>/lock`, with a conditional write that only succeeds when no lock exists. It holds who took it, the host or the url of the ci run, the command and the time. A second deploy of the same stage finds the lock and stops at once, saying who holds it and how to release it:

```text
error: orders/prod is locked: levi on build-7 has been running deploy since 14:02:11, 3m4s ago
wait for it to finish, or, when it is gone, release the lock with kdst unlock --stage prod
```

The lock is removed when the deploy ends, whether it succeeded or not, also when it was stopped with ctrl+c or a SIGTERM. `plan` and `outputs` take no lock.

A lock outlives its deploy only when the process was killed. `kdst unlock` removes it, after showing who took it and when and asking, or without asking with `--yes`; the ui's `ctrl+x` does the same. Every write of the state is also conditional, on the version the deploy last read, so a deploy that went on after its lock was released stops at its next write instead of overwriting another's.

## failures

Every step is recorded in the state twice: as pending before the call to aws, and with what exists after it. A deploy that fails, or is stopped halfway, leaves a state that says what may exist, and running the deploy again finishes the work:

- a resource koldstart names, such as a function or a role, is looked up by its name, and taken over when its tags say this node created it
- a resource cloud control makes is found by the request the deploy recorded, and its create is sent with the same client token, so cloud control makes it once however often it is tried
- an event source mapping, which has no name, is found by its function and its source

There is no automatic rollback. A half applied change is finished by the next deploy, or undone by deploying the previous configuration.

## removing

`kdst remove` prints the plan of deleting everything the stage's state lists, asks you to type the stage's name, or goes on with `--yes`, and deletes it in reverse dependency order, with the same progress as a deploy and `removed orders/pr-12 in 12.3s: 9 deleted` at the end. Resources that hold data, and imported ones, are left in place and stay in the state, listed with their ids in aws under `left in place, for the next deploy of the stage to take over again:`, so the next deploy of the stage takes them over again. When nothing is left, the state is deleted too, and a second remove says `nothing of orders/pr-12 is deployed`. The state bucket and the zips are left in place, the bucket because other apps and stages use it.
