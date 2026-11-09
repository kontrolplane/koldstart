# secrets

A secret is an environment variable whose value koldstart reads from ssm parameter store or secrets manager, so it is never in the configuration or the state. `kdst.param()` and `kdst.secret()` name one under the app and stage, so each stage has its own; `kdst.ssm()` and `kdst.secretsmanager()` name one by its full name, for what every stage or app shares. `.field("key")` takes one key of a json secret.

```pkl
environment {
  ["PROVIDER_KEY"] = kdst.param("provider-key")
  ["DATABASE_PASSWORD"] = kdst.secret("database").field("password")
  ["ERROR_REPORTING_URL"] = kdst.ssm("/shared/error-reporting-url")
  ["WEBHOOK_SECRET"] = kdst.param("webhook-secret").readAtRuntime()
  ["PARTNER_TOKEN"] = kdst.secretsmanager("shared/partner").field("token").readAtRuntime()
}
```

| written                                 | read from, on `development`                       |
| --------------------------------------- | ------------------------------------------------- |
| `kdst.param("provider-key")`            | the parameter `/secrets/development/provider-key` |
| `kdst.secret("database")`               | the secret `secrets/development/database`         |
| `kdst.ssm("/shared/error-reporting-url")` | the parameter `/shared/error-reporting-url`     |

A secret read while deploying becomes the variable's value: a plan reads it again and shows when it changed, and the next deploy takes it up. Anyone who may read the function's configuration sees it. With `.readAtRuntime()` the variable holds the parameter's or secret's arn instead, the role may read that one, and the function reads it itself, as [src/payments.py](./src/payments.py) does, caching it for five minutes so a rotated value is taken up without a deploy. With a `.field()`, `<NAME>_KEY` says which key. A deploy stops before it changes anything when a secret does not exist, and names it.

## trying it

```bash
aws ssm put-parameter --name /secrets/development/provider-key --type SecureString --value "$(openssl rand -hex 32)"
aws ssm put-parameter --name /secrets/development/webhook-secret --type SecureString --value "$(openssl rand -hex 32)"
aws ssm put-parameter --name /shared/error-reporting-url --type String --value https://errors.example.com/ingest
aws secretsmanager create-secret --name secrets/development/database --secret-string '{"username": "payments", "password": "change-me"}'
aws secretsmanager create-secret --name shared/partner --secret-string '{"token": "change-me"}'
kdst plan --stage development
kdst deploy --stage development
kdst invoke --stage development --function payments
```

The plan shows each secret by its full name, as `(secret ssm:/secrets/development/provider-key)`, never its value. The function answers with the length of each value it has, read while deploying and read now. Put a new value with `--overwrite` and invoke it again after five minutes: the runtime ones change without a deploy; the others change with the next one.

A parameter encrypted with a kms key of your own also needs `kms:Decrypt` on that key, through [permissions](../permissions).

## related

- [permissions](../permissions): access a function needs besides its secrets
- [encryption-keys](../encryption-keys): a key of your own
- [link](../link): a secrets manager secret of the project, by name
