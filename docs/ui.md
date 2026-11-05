# terminal interface

> [!NOTE]
> This is the design of `kdst ui` in [#1](https://github.com/kontrolplane/koldstart/issues/1), which comes after `migrate`. It is not built yet.

`kdst ui` opens a terminal interface for everything koldstart has deployed. It finds apps and stages by reading the state buckets in the accounts and regions your profiles reach, so it works from any directory and needs no configuration file. Opened in a project directory, it starts on that project's app.

## views

- `app`: every app and its stages, with when each stage was last deployed, by whom, and whether it is locked
- `stage`: the functions, resources and outputs of a stage, plan, deploy and remove
- `function`: its configuration, events and code hash, its logs, and invoking it
- `plan`: what a deploy would change, and the progress of each resource while it applies
- `history`: earlier versions of a stage's state, and what changed between them

The header shows the aws profile, region and account in use, and the state bucket.

## keybindings

- `q`: back, quit on the app overview
- `esc`: back, clear the filter or selection, cancel a dialog or form; never quits
- `ctrl+c`: quit
- `↑`, `k`: up
- `↓`, `j`: down
- `→`, `l`: right
- `←`, `h`: left
- `g`, `G`: first/last row, top/bottom of the logs
- `pgup`, `pgdn`: page up/down
- `tab`, `shift+tab`: switch between functions, resources and outputs; on a function between its configuration, logs and invocations
- `d`: plan a deploy of the stage, or of the function's code
- `ctrl+s`: apply the plan that is shown
- `ctrl+d`: remove the stage
- `ctrl+x`: release a stale lock
- `i`: invoke the function, `shift+i` to run it locally
- `e`: edit the event to invoke with in `$EDITOR`
- `f`: follow the newest log lines, or stop following
- `y`, `n`: answer a yes/no dialog
- `c`: copy the arn, url or output under the cursor
- `r`: refresh, also while paused
- `p`: pause/resume the automatic refresh
- `?`: help
- `enter`: view
- `/`: filter

Removing a stage asks to type its name first, and releasing a lock shows who took it and when before asking. Applying a plan applies the plan on screen. When the configuration or the state changed since it was made, it is made again and shown before anything is applied.

Events used to invoke a function are kept in `.koldstart/events/<function>/` in the project, as json files, so they can be committed and shared.

## flags

| flag        | description                                                                              |
| ----------- | ---------------------------------------------------------------------------------------- |
| `--theme`   | `auto`, the default, follows the terminal; `dark` and `light` paint their own background |
| `--debug`   | write debug logs to `debug.log`                                                          |

The global `--stage`, `--profile` and `--region` flags pick where the interface opens.
