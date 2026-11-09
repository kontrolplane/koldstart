# defaults

`functions { default { ... } }` is what every function of the project gets unless it sets its own: memory, timeout, how its logs are written and kept, and tracing. It keeps a project of many functions consistent, and a function that needs something else says only that.

```pkl
functions {
  default {
    memory = 256
    timeout = 10.s
    logRetention = 30
    logFormat = "json"
    applicationLogLevel = "INFO"
    systemLogLevel = "WARN"
    tracing = true
  }

  ["lookup"] {}

  ["export"] {
    memory = 1024
    timeout = 2.min
  }
}
```

`default` is not a function: it is what each entry beside it amends. `lookup` is the default as it is; `export` takes 1024 megabytes and two minutes, and keeps the json logs, the levels, the retention and the tracing. Without `default`, a function gets koldstart's own: 1024 megabytes, six seconds, text logs kept for 30 days and no tracing.

- `logFormat = "json"` makes lambda write each line as json, with its time, level and request id, which logs insights queries by field.
- `applicationLogLevel` keeps the function's own lines from that level up, here info: the debug line is dropped by lambda, without a change of code. `systemLogLevel` does the same for lambda's own lines.
- `tracing = true` sends traces to x-ray, and lets the role write them.

## trying it

```bash
kdst deploy --stage development
kdst invoke --stage development --function lookup --path .koldstart/events/lookup/customer.json
kdst invoke --stage development --function export --path .koldstart/events/export/month.json
kdst logs --stage development --function lookup
```

`lookup` answers with 256 megabytes and about ten seconds, in milliseconds, left; `export` with 1024 and about two minutes. The log holds the line `looked a customer up` at `INFO`, as json with its `customer`, and not the debug line before it. `kdst remove --stage development` deletes the stage.

## related

- [stages](../stages): defaults that differ between the stages
- [tags](../tags): what every resource is tagged with
- [runtimes](../../runtimes): the same settings in each language
