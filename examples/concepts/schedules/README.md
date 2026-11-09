# schedules

`kdst.schedule()` runs a function on a schedule of eventbridge scheduler: a `cron(...)` at set times, a `rate(...)` every so often, or an `at(...)` once. The function gets `input` as its event, so one function can tell its schedules apart, and `enabled = false` keeps a schedule while it runs nothing, here on every stage but production.

```pkl
(kdst.schedule("cron(30 7 ? * MON-FRI *)")) {
  timezone = "Europe/Amsterdam"
  input = #"{"kind": "weekday"}"#
  enabled = kdst.stage == "production"
}
(kdst.schedule("rate(15 minutes)")) { input = #"{"kind": "frequent"}"# }
(kdst.schedule("at(2026-12-31T23:00:00)")) { timezone = "Europe/Amsterdam" }
```

A `cron` takes six fields, minutes to years, with `?` in the day of the month or of the week. Its times are in `timezone`, `UTC` without it, and the scheduler follows summer time there: 07:30 in Amsterdam stays 07:30 all year, where `cron(30 6 ...)` in UTC would be 07:30 in winter and 08:30 in summer.

The scheduler hands the function its event without waiting, so lambda tries a failed run twice more. [dead-letter-queues](../dead-letter-queues) keeps the runs that failed every time, and [alarms](../alarms) pages when a schedule stops running at all.

## trying it

```bash
kdst deploy --stage development
kdst invoke --stage development --function report --path .koldstart/events/report/weekday.json
kdst logs --stage development --function report --since 1h
```

`invoke` runs it now, with the weekday schedule's event, and answers `{"kind": "weekday", "ranAt": "..."}`. The logs show a `frequent` run every fifteen minutes. On `--stage production` the weekday report runs too; `aws scheduler list-schedules --name-prefix schedules-development` lists the three schedules of the stage.

## related

- [dead-letter-queues](../dead-letter-queues): the runs that failed every time
- [alarms](../alarms): a heartbeat for a schedule that stopped
- [stages](../stages): what differs between the stages
