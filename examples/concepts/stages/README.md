# stages

A stage is one deployment of the app: `development`, `staging` and `production`, or the preview of a pull request. Each has its own functions, resources and state, and one `koldstart.pkl` describes them all: `kdst.stage` is what `--stage` names, so what differs between the stages is written down once, in [koldstart.pkl](./koldstart.pkl).

```pkl
local stages: Mapping<String, Dynamic> = new {
  ["development"] { memory = 256; logRetention = 14; reporting = false }
  ["production"] { memory = 1024; logRetention = 365; reporting = true }
}

local stage = stages.getOrNull(kdst.stage) ?? stages["development"]

functions {
  ["report"] {
    memory = stage.memory
    events {
      (kdst.schedule("cron(0 6 * * ? *)")) { enabled = stage.reporting }
    }
  }
}
```

`stages.getOrNull(kdst.stage) ?? stages["development"]` gives any stage the mapping does not name development's settings, so `--stage pull-request-142` deploys a small copy of the app of its own, which `kdst remove --stage pull-request-142` deletes again. Without `--stage` or `KOLDSTART_STAGE`, koldstart picks `dev`, which is sized as development too: pass `--stage` every time.

|                   | development | staging   | production |
| ----------------- | ----------- | --------- | ---------- |
| memory            | 256 megabytes | 512 megabytes | 1024 megabytes |
| log retention     | 14 days     | 30 days   | 365 days   |
| the 06:00 report  | off         | off       | on         |

## trying it

```bash
kdst plan --stage development
kdst plan --stage production
kdst deploy --stage development
kdst invoke --stage development --function report
```

The two plans differ only in what the table says. `invoke` answers `{"stage": "development", "production": false, "memoryMegabytes": 256}`. Deploy `--stage production` and the same function answers with 1024, and runs every morning at 06:00 in London. `kdst remove --stage development` deletes the stage.

## related

- [defaults](../defaults): what every function of a stage gets
- [tags](../tags): the stage in every resource's tags
- [stage-outputs](../stage-outputs): one app reading another's stage of the same name
