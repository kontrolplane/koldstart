# terminal interface

`kdst ui` opens a terminal interface for everything koldstart has deployed in an account. It finds the apps and stages by listing the account's state buckets, one for each region, so it works from any directory and needs no configuration file. Without permission to list buckets, it reads the bucket of the region in use alone.

```bash
kdst ui
kdst ui --profile prod --stage prod
```

Opened in a project's directory, it starts on the project's stage, `--stage` or `dev`, also before its first deploy. The project's app is marked `●`: its stages can be planned and deployed, and its functions run on this machine. Any stage can be browsed, invoked, unlocked and removed.

The header shows the aws profile, account, region and state bucket in use, the project here, and who the credentials belong to.

## views

- `stages`: every app and stage, with when it was last deployed, by whom, how many resources it has, and who holds its lock
- `stage`, in tabs:
  - functions, with their invocations, errors and p99 duration of the last hour
  - resources, as a tree: each function with its role, log group, url, permissions, routes and events, each resource with the notifications of a bucket and the depth of a queue, and the http api with its domain and authorizers
  - outputs
  - history: the deploys, each the versions of the state it wrote; one shows what it changed from the deploy before, and each change opens for its full diff
- `function`, in tabs:
  - overview: its configuration; what lambda says of it now, its state, its last update, and code or configuration changed outside koldstart; sparklines of its invocations, errors, throttles, duration and concurrency in the last hour; its environment and what invokes it
  - logs: the last half hour, followed with a live tail, or read again every few seconds where there is none; json and logfmt tinted so the message stands out; `/` filters the lines, and ↑ at the top reads the hour before
  - invocations: the requests in the logs read, with their duration, billed duration, memory, cold start, and whether they failed or timed out; one opens with its lines of the log
  - invoke: the events kept for it, and what the last invocation with each returned
- `resource`: what koldstart applied, its id in aws, its attributes and inputs, and on a second tab its properties in aws now, from cloud control, with the ones changed outside koldstart
- `plan`: what a deploy or remove does, and the progress of each resource while it applies; each change opens for its full diff of inputs, with secrets and refs shown by what they stand for

The page on screen is read again every five seconds; `p` pauses that. The live data, metrics, lambda's own view of a function, a queue's depth and a resource's properties in aws, needs the permissions to read them; where it cannot be read, as on an emulator, the page says so and shows the rest.

## keybindings

- `q`: back, quit on the stages
- `esc`: back, clear the filter, cancel a dialog; never quits
- `ctrl+c`: quit
- `↑`, `k` and `↓`, `j`: up and down
- `g`, `G`: first and last row, top and bottom of the logs
- `pgup`, `pgdn`: page up and down
- `tab`, `shift+tab`: switch between functions, resources, outputs and history; on a function between its overview, logs, invocations and invoke; on a resource between what was applied and what is in aws
- `enter`: open: a stage, a function, a resource, a deploy, an invocation, or a change's diff
- `/`: filter a table, or the lines of the logs
- `d`: plan a deploy of the stage, or on a function of its code
- `ctrl+s`: apply the plan on screen
- `ctrl+d`: remove the stage
- `ctrl+x`: release a lock
- `i`: invoke the function in aws, `shift+i` to run it on this machine
- `e`: edit the event to invoke with in `$VISUAL` or `$EDITOR`
- `f`: follow the newest log lines, or stop following
- `y`, `n`: answer a yes/no dialog
- `c`: copy the arn, id, output or payload under the cursor
- `r`: refresh
- `p`: pause or resume the refresh
- `?`: help

`ctrl+s` applies the plan on screen. It takes the stage's lock and plans again first: when the configuration or the state changed since, nothing is applied, and the new plan is shown to apply instead. Removing a stage shows the plan of the remove, and asks to type the stage's name. Releasing a lock shows who took it, for what, and when, before asking. Running a function of `prod` on this machine asks first.

Quitting while a deploy or remove runs stops it, as ctrl+c does on the command line: the lock is released, and the state says what was done, so running it again finishes it.

Events to invoke a function with are kept in `.koldstart/events/<function>/` in the project, as json files, so they can be committed and shared. Without any, the function is invoked with `{}`, and `e` creates `event.json` to edit.

## flags

| flag      | description                                                                              |
| --------- | ---------------------------------------------------------------------------------------- |
| `--theme` | `auto`, the default, follows the terminal; `dark` and `light` paint their own background |

The global `--stage`, `--profile`, `--region` and `--debug` flags work as they do everywhere: `--stage` picks the project's stage it opens on.
