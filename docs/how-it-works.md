# how it works

> [!NOTE]
> This describes the design in [#1](https://github.com/kontrolplane/koldstart/issues/1). Nothing here is implemented yet.

A deploy goes through the same steps every time. The configuration is evaluated, the functions are built, the result is compared with what the state says was deployed, and the difference is applied with direct calls to the AWS APIs.

```text
koldstart.pkl ──► evaluate ──► build + zip ──► desired graph
                                                    │
                     s3: <app>/<stage>/state.json ──┤ diff
                                                    ▼
                                                  plan ──► kdst plan prints it
                                                    │
                                                    ▼ kdst deploy
                                  apply, in dependency order
                                  ├── lambda, iam, api gateway, scheduler, ...
                                  └── cloud control for everything in resources
                                                    │
                                                    ▼
                                  state written after every step
```

## planning

The configuration becomes a graph. Every function, event and resource is a node, together with the iam role, log group and permissions that come with it. Every `ref()` and every implicit dependency, such as a route needing its api, is an edge. Each node is compared with its entry in the state, by a hash of its inputs, and ends up as one of:

- `create`: in the configuration, not in the state
- `update`: in both, with different inputs, and the change can be made in place
- `replace`: in both, but a property changed that aws does not allow to be changed in place, such as a table's key schema
- `delete`: in the state, no longer in the configuration
- no change

`kdst plan` prints this list and touches nothing. It does not read the live resources, so a change made by hand in the console is not part of the plan. `kdst plan --refresh` reads every resource first and shows the difference as drift.

## applying

`kdst deploy` applies the plan in dependency order, working on resources that do not depend on each other at the same time. Deletes go the other way, so nothing is removed while something still points at it.

When the only difference is the code hash of one or more functions, the deploy uploads the new zips and calls `UpdateFunctionCode`, without planning anything else. `kdst deploy function -f <fn>` always takes this path, for that one function.

## state

The state lives in a bucket in your account, `koldstart-state-<account>-<region>`, which the first deploy creates with versioning and encryption turned on and public access blocked. Each stage has one object, `<app>/<stage>/state.json`, which lists every resource with its type, its name in the configuration, its id in aws, the hash of its inputs and its attributes. The zips are uploaded next to it, under `artifacts/<app>/<stage>/<hash>.zip`.

Because the bucket is versioned, every deploy leaves the previous state behind as an older version of the object. `kdst ui` lists them.

## locking

A deploy or remove first writes `<app>/<stage>/lock`, with a conditional write that only succeeds when no lock exists. The lock holds who took it, when, and from where. A second deploy of the same stage finds the lock and stops, and tells you who holds it. The lock is removed when the deploy ends, whether it succeeded or not.

A lock outlives its deploy only when the process was killed. `kdst unlock` removes it, after showing who took it and when. Take care that the deploy is really gone: two deploys applying to the same stage at once can leave its state wrong.

## failures

The state is written after every step that changed something in aws, not once at the end. A deploy that fails or is interrupted therefore leaves state that matches what exists. Running the deploy again plans from there and finishes the work. There is no automatic rollback: a half applied change is completed by the next deploy, or undone by deploying the previous configuration.

## removing

`kdst remove` deletes everything the stage's state lists, in reverse dependency order, and then the state object. Removing asks to type the stage name first. The state bucket and the uploaded zips are left in place, the bucket because other apps and stages use it.
