# alarms

An alarm watches a metric and tells someone when it goes wrong: here every alarm notifies an `alerts` topic, and on production pages the on-call address subscribed to it. They are cloudwatch alarms in `resources`, made by a local `alarm()` for what they share, and generated from `functions` with a `for`, so a function added later gets its own.

```pkl
for (name, definition in functions) {
  ["\(name)-errors"] = (alarm("\(name)-errors", "errors", "more than 1% of the invocations of \(name) failed")) { ... }
  ["\(name)-throttles"] = (alarm("\(name)-throttles", "throttles", "lambda turned invocations of \(name) away")) { ... }
  ["\(name)-duration"] = (alarm("\(name)-duration", "duration", "the slowest 1% of invocations of \(name) are near its timeout")) {
    properties { Threshold = definition.timeout.toUnit("ms").value * 0.8 }
  }
}
```

- `errors` is a rate, with metric math over `Errors` and `Invocations`, so one failure among thousands does not page and one in ten does.
- `duration` compares the 99th percentile with 80% of the function's own timeout, read from `definition`, so it follows a change of timeout.
- `nightly-failures-depth` fires when the dead-letter queue holds a run that failed every time, and `nightly-heartbeat` when `nightly` has not run for 24 hours. A missing hour counts as breaching, so it fires when the schedule stops, which no error alarm sees. The schedule runs twice a day, so one missed run is made up before it pages.
- `stages` decides where alarms page: `ActionsEnabled` is on for production alone, and only production subscribes `on-call@example.com`. development and staging have the same alarms, which show in cloudwatch and notify no one.
- each description ends with a link to the section of the runbook below named by `alarm()`'s second argument.

## trying it

```bash
kdst deploy --stage development
curl "$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)/?fail=true"
aws cloudwatch describe-alarms --alarm-name-prefix alarms-development
```

A few failing requests in three of five minutes put `alarms-development-api-errors` in `ALARM`. On production the subscription sends a confirmation email first; once confirmed, the address gets each change of state. A real on-call tool subscribes the `alertsTopic` output with its own https endpoint.

## runbook

### errors

More than 1% of a function's invocations failed. `kdst logs --stage <stage> --function <function> --since 15m` shows them.

### throttles

Lambda turned invocations away: the account or the function ran out of concurrency.

### duration

The slowest invocations take more than 80% of the timeout: what the function calls is slowing down, or the timeout is too short.

### dead-letters

`nightly-failures` holds a run that failed every time: see [dead-letter-queues](../dead-letter-queues) to read it and run it again.

### heartbeat

`nightly` has not run for a day: check its schedule is enabled, with `aws scheduler list-schedules --name-prefix alarms-<stage>`.

## related

- [dead-letter-queues](../dead-letter-queues): what `nightly-failures` holds
- [schedules](../schedules): what the heartbeat watches
- [stages](../stages): settings that differ between the stages
