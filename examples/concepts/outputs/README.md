# outputs

`outputs` are the values of a deployed stage that people and other apps need: a bucket's name, a url, an arn. `kdst outputs` prints them, `kdst ui` shows them on the stage's outputs tab, and another app reads them with [`kdst.stageOutput()`](../stage-outputs). They are worked out from the stage's state after each deploy, so reading them asks no service but the state bucket.

```pkl
outputs {
  ["uploadsBucket"] = kdst.ref("uploads", "BucketName")
  ["uploadFunction"] = kdst.ref("upload", "FunctionName")
  ["uploadsPolicyResource"] = kdst.join(List("arn:", kdst.partition, ":s3:::", kdst.ref("uploads", "BucketName"), "/*"))
  ["account"] = kdst.accountId
  ["runbook"] = "https://wiki.example.com/uploads/runbook"
}
```

- a string is printed as it is.
- `kdst.ref(name, attribute)` is an attribute of a function or resource of the project once it exists, as cloudformation's `Fn::GetAtt` names it for the type.
- `kdst.join(List(...))` joins strings, refs and the account's values, `kdst.accountId`, `kdst.partition` and `kdst.urlSuffix`, into one string.

A secret or another stage's output cannot be an output: the state keeps no secrets, and an output is read from this stage alone.

koldstart adds outputs of its own, from what the stage deploys: `httpApiUrl` once a function has an http route, as `upload` has, with `httpDomainTarget` when the http api has a domain; `restApiUrl` once one has a rest route, with `restApiKeyId` when one is private; `webSocketUrl` once one has a websocket route; and `<function>Url` for each function url, `stripeWebhookUrl` for `stripe-webhook`.

## trying it

```bash
kdst deploy --stage development
kdst outputs --stage development
URL="$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl --request POST "$URL/uploads" --data 'hello'
aws s3 ls "s3://$(kdst outputs --stage development --output json | jq --raw-output .uploadsBucket)/uploads/"
```

`kdst outputs` prints one `name = value` line for each, the six of [koldstart.pkl](./koldstart.pkl) and `httpApiUrl`; with `--output json`, one object of them, for `jq` and scripts. The `curl` answers with the key it stored the body under, which `aws s3 ls` then lists. `kdst remove --stage development` deletes the stage, and leaves the bucket in place, as buckets hold data.

## related

- [stage-outputs](../stage-outputs): another app reading an output of this one
- [stages](../stages): every stage has outputs of its own
