# terminal interface

`kdst ui` opens a terminal interface for everything koldstart has deployed in an account. It finds the apps and stages in the account's state buckets, `koldstart-state-<account>-<region>`, one for each region, so it works from any directory and needs no configuration file. When it cannot list buckets, it reads the bucket of the region in use alone.

```bash
kdst ui
kdst ui --profile prod --stage prod
```

Opened next to a `koldstart.pkl`, or the file `--config` names, it starts on the project's stage, `--stage`, `KOLDSTART_STAGE` or `dev`, also before its first deploy. The project's app is marked `●`: its stages can be planned and deployed, and its functions run on this machine. Any stage can be browsed, invoked, unlocked and removed. A configuration that does not load says why, and the ui opens on the stages without a project.

The header shows the aws profile, `default` without one, the account, the region, the state bucket, the project and its stage, or `none here`, and who the credentials belong to, as `user/levi`. It needs a terminal of at least 78 by 22; a smaller one says so, and only `ctrl+c` works.

## views

- `stages`: every app and stage, with its region, when it was last deployed and by whom, how many resources it has and how many were kept by a remove, and who holds its lock, or `free`. A stage whose state or lock cannot be read is `✗ unreadable`, and `enter` shows why; one of the project never deployed is `not yet`. Without any: `no stages in this account yet: deploy one with kdst deploy`.
- `stage`: when it was last deployed, by whom, its serial and region, and who holds its lock, in tabs:
  - functions: runtime, memory, timeout, code, and the invocations, errors and p99 duration of the last hour; `…` while they are read, and `-` where they cannot be, with `could not read the metrics: <error>` beside the tabs
  - resources, as a tree: each function with its role, log group, url, permissions, routes and events, each resource with the notifications of a bucket and the depth of a queue, and the http api with its domain and authorizers; a note says what is pending, kept, imported or retained. A function opens the function page, the rest the resource page
  - outputs; a value of more than one line shows its first and how many more, and `c` copies all of it
  - history: the deploys, each the versions of the state it wrote; one opens with who deployed it, when, its serial and version, and the resources and outputs it changed from the deploy before, counted as `+1 added  ~2 changed  -1 removed`; each changed resource opens for its diff
  - an empty tab says `no functions yet`, `no resources yet`, `no outputs yet` or `no deploys yet`, and `no function matches <filter>: esc clears the filter` when the filter hides every row
- `function`, in tabs:
  - overview: its name, and its runtime, architecture, memory, timeout, the stage's last deploy and its state in lambda on a line below, with a notice when its code, memory, timeout or runtime was changed outside koldstart; then its configuration; what lambda says of it now, its state and its last update, and each setting that differs from what koldstart deployed; sparklines of its invocations, errors, throttles, p99 duration and concurrency in the last hour, each with its total, its range and its last minute, or `not invoked in the last hour`; what invokes it, its url first, each by its kind; its environment, sorted, values that look like secrets hidden and secrets, refs and outputs by what they stand for; and its role, with its managed policies and what its policy allows. Two columns from a terminal 111 wide, one below it. Long arns, names and urls are cut in the middle; `c` copies the whole value of the row under the cursor, the name of a variable whose value is hidden, and `enter` opens its log group, role or a trigger on the resource page, or its url in the browser
  - logs: the last half hour, followed with a live tail, or read again every five seconds where there is none; json and logfmt tinted so the message and an error level stand out, long lines wrapped under their time; `/` filters the lines and marks what it looks for, and `↑` or `pgup` at the top reads the hour before. Without a log group: `no log group: deploy the function first`
  - invocations: the requests in the logs read, with their duration, billed duration, memory, cold start, and whether they failed or timed out; one opens with its lines of the log. Without any: `no invocations in the logs read: ↑ on the logs tab reads further back`
  - invoke: the `.json` events kept for it, with the ones that are not json marked, and what the last invocation with each returned or failed with, and its log
- `resource`: what koldstart applied, its id in aws, its policy, what it depends on, its attributes and inputs, and on a second tab, `in aws`, its properties in aws now, from cloud control, with the ones changed outside koldstart, what each was applied with and what it is now
- `plan`: what a deploy or remove does, and the progress of each resource while it applies, with how long it took and the error of one that failed; each change opens for its diff. A plan with nothing to do says `<app>/<stage> is as the configuration says: there is nothing to deploy`
- `diff`: the inputs of a resource before and after, as indented json, opened on its first change:
  - a line under the title counts the fields added, changed and removed, as `+1 added  ~2 changed  -1 removed`, or says `no change`, and `no change but the spacing of json in a string` when that is all that differs
  - a field changed is its removed line right above its added one, with the part that changed marked, as the number of `"memory": 512` to `"memory": 1024`, or `dev` to `prod` in an arn
  - three lines are kept around each change, and the rest folded into rows such as `⋯ 24 unchanged lines`
  - json in a string, as a policy document often is, is written out after `(json)` and diffed as json, so one statement changed is one line; the order of keys is no change, and an item added to a list marks only that item
  - long values wrap under their key, with the `+` or `-` on every line; secrets, refs and outputs show in parentheses by what they stand for, as `(secret ssm:/orders/key)`
  - the change navigated to is marked `▌`, and the frame's foot says which it is, as `change 2 of 5`

The stages, a stage and a function are read again every five seconds, and `p` pauses that; the other pages are read once, and `r` reads them again. The live data, metrics, lambda's own view of a function, a queue's depth and a resource's properties in aws, needs the permissions to read them; where metrics, lambda, cloud control or a queue's depth cannot be read, as on an emulator, the page says so, as `could not read the metrics: <error> · r tries again`, and shows the rest. When a read fails after one that worked, what that one read stays on screen, marked `as of 10:04:05, could not read again`.

## keybindings

- `q`: back, quit on the stages; on a dialog, no
- `esc`: back, clear the filter, cancel a dialog or a plan being made, where the plan `r` asked for goes back to the one before; never quits
- `ctrl+c`: quit, also while a deploy or remove runs
- `↑`, `k` and `↓`, `j`: up and down
- `g`, `home` and `G`, `end`: first and last row, top and bottom of the logs, where `G` and `end` follow them again
- `pgup`, `pgdn`: page up and down; on the invoke tab, scroll what the invocation returned
- `tab`, `shift+tab`: switch between functions, resources, outputs and history; on a function between its overview, logs, invocations and invoke; on a resource between what was applied and what is in aws; on a dialog between no and yes
- `←`, `h` and `→`, `l`: switch the tabs of a stage, a function and a resource, as `tab` and `shift+tab` do; on a dialog, choose no or yes
- `enter`: open a stage, a function, a resource, a deploy, an invocation, or a change's diff; on a function's overview, the log group, role or trigger of the row, or its url in the browser; on a diff, show the lines of the first fold in sight; apply a filter; answer a dialog with the button chosen
- `]`, `[`: on a diff, go to the next or the previous change
- `z`: on a diff, show every line, or fold them again
- `/`: filter the stages, the tabs of a stage, or the lines of the logs
- `d`: plan a deploy of the project's stage, or on a function of its code
- `ctrl+s`: apply the plan on screen
- `r` on a plan of a deploy of the stage: plan again with every resource read as it is in aws, to find what was deleted or changed outside koldstart
- `ctrl+d`: remove the stage, on the stages and on a stage
- `ctrl+x`: release the stage's lock, on the stages and on a stage
- `i`: invoke the function in aws, `shift+i` to run it on this machine, from any tab of a function
- `e`: edit the event to invoke with in `$VISUAL`, `$EDITOR` or `vi`
- `f`: on the logs, follow the newest lines, or stop following
- `y`, `n`: answer the dialog to release a lock or to run against prod
- `c`: copy what is under the cursor: an app and stage, a function's arn, the whole value of a row of its overview, a resource's id in aws, an output, a version, a request id, or what the last invocation returned
- `r`: read the page again
- `p`: pause or resume reading the page again
- `?`: help; any key closes it

While a filter, or the stage's name to remove it, has focus, letters, `?` and `p` included, are typed into it.

`ctrl+s` applies the plan on screen. It takes the stage's lock and plans again first: when the configuration or the state changed since, nothing is applied, and the new plan is shown to apply instead. Removing a stage shows the plan of the remove, and asks to type the stage's name, such as `dev`. Releasing a lock shows who took it, for what, from where, and when, before asking. Running a function of `prod` on this machine asks first, naming the event it sends.

While a deploy or remove runs, `q` and `esc` stay on the plan. `ctrl+c` stops it, as it does on the command line: the lock is released, and the state says what was done, so running it again finishes it.

Events to invoke a function with are kept in `.koldstart/events/<function>/` in the project, as `.json` files, so they can be committed and shared. Without any, the function is invoked with `{}`, and `e` creates `event.json` to edit. Without a project here there is nowhere to keep them: `{}` is sent, and `e` says so. An event that is not json is not sent.

## flags

| flag      | description                                                                                                                                                                    |
| --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `--theme` | `auto`, the default, follows the background the terminal reports, or `COLORFGBG`, and is dark when neither says; `dark` and `light` paint their own background into every cell |

The colours follow what the terminal shows: truecolor, 256 colours, its own 16, or none with `NO_COLOR`. In 16 colours or none, the row under the cursor is drawn in reverse video, and without colour, failures and warnings are bold.

The global `--stage`, `--config`, `--profile`, `--region` and `--debug` flags work as they do everywhere: `--stage` picks the project's stage it opens on, and `--config` the project.
