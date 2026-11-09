# tags

`tags` puts the same tags on everything a stage creates: functions, roles, log groups, the apis and every resource. Cost reports split the bill by them, tag policies check them, and people find what belongs to whom. A function's own `tags` add to the project's on that function, and win where both name a key.

```pkl
tags {
  ["service"] = "invoices"
  ["team"] = "billing"
  ["owner"] = "billing@example.com"
  ["cost-centre"] = "4120"
  ["environment"] = environments.getOrNull(kdst.stage) ?? "development"
  ["data-classification"] = "confidential"
}

functions {
  ["render"] {
    tags {
      ["data-classification"] = "restricted"
      ["on-call"] = "billing-primary"
    }
  }
}
```

Next to them, koldstart tags everything it creates with its own: `koldstart:app`, `koldstart:stage` and `koldstart:node`, the id of what made it, such as `function/render`. It reads them back to tell its resources from anyone else's, so they are not yours to set.

## trying it

```bash
kdst deploy --stage development
aws resourcegroupstaggingapi get-resources \
  --tag-filters Key=koldstart:app,Values=tags Key=koldstart:stage,Values=development \
  --query 'ResourceTagMappingList[].ResourceARN' --output text
aws lambda list-tags --resource "$(aws lambda get-function --function-name tags-development-render --query Configuration.FunctionArn --output text)"
```

The first lists every resource of the stage: the two functions, their roles and log groups, and the bucket. The second shows `render` with `data-classification` set to `restricted` and an `on-call` tag, where `archive` has the project's tags alone. `kdst remove --stage development` deletes the stage, and leaves `invoices` in place, as buckets hold data.

## related

- [stages](../stages): what differs between the stages, including the tags
- [defaults](../defaults): what every function gets
