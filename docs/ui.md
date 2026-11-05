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
- `stage`: its functions, resources, outputs and the history of its state, in tabs
- `function`: its configuration, environment and what invokes it; its logs, read every few seconds while following; and invoking it
- `resource`: its id in aws, attributes, and the inputs it was last applied with
- `history`: an earlier version of the state, and what it changed from the one before
- `plan`: what a deploy or remove does, and the progress of each resource while it applies

The page on screen is read again every five seconds; `p` pauses that.

## keybindings

- `q`: back, quit on the stages
- `esc`: back, clear the filter, cancel a dialog; never quits
- `ctrl+c`: quit
- `↑`, `k` and `↓`, `j`: up and down
- `g`, `G`: first and last row, top and bottom of the logs
- `pgup`, `pgdn`: page up and down
- `tab`, `shift+tab`: switch between functions, resources, outputs and history; on a function between its configuration, logs and invoke
- `enter`: open
- `/`: filter
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
