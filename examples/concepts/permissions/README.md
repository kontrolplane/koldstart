# permissions

`permissions` adds statements to a function's role, for access [link](../link) does not give: a resource outside the project, a path of parameters, a type link does not know, or an action that takes no resource. Each statement names its actions and the resources they are for; `effect = "Deny"` takes access away, and wins over any allow.

```pkl
permissions {
  new {
    actions { "ssm:GetParametersByPath"; "ssm:GetParameter" }
    resources { parameters("settings"); parameters("settings/*") }
  }
  new {
    effect = "Deny"
    actions { "ssm:GetParameter*" }
    resources { parameters("settings/internal/*") }
  }
  new {
    actions { "events:PutEvents" }
    resources { kdst.ref("prices", "Arn") }
  }
}
```

A resource is a string, a `kdst.ref()` to one of the project's resources, which waits for it to exist, or a `kdst.join()` of both with `kdst.partition` and `kdst.accountId`, so the arn holds in any account and partition the stage is deployed to. `parameters()` is a local function of [koldstart.pkl](./koldstart.pkl) that builds one. Keep `*` for actions that take no resource, as cloudwatch's `PutMetricData`.

## trying it

```bash
aws ssm put-parameter --name /permissions/development/settings/currency --type String --value EUR
aws ssm put-parameter --name /permissions/development/settings/internal/margin --type String --value 0.3
kdst deploy --stage development
kdst invoke --stage development --function pricing
```

It answers with the settings under `settings/`, `currency` alone, and `["AccessDeniedException"]` for `internal/margin`, which the deny statement refuses although the first allows it. The event reaches the stage's bus, and the count shows in cloudwatch under `Pricing`.

## related

- [link](../link): access to the project's own resources, without writing a statement
- [secrets](../secrets): parameters koldstart reads for the function
- [encryption-keys](../encryption-keys): a statement on a key of the project
