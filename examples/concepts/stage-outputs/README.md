# stage-outputs

Two apps, each with a `koldstart.pkl`, a state and stages of its own: [payments](./payments) takes charges, and [shop](./shop) charges through it. shop does not hard-code payments' url: it reads the output `httpApiUrl` of payments' stage from payments' state, with `kdst.stageOutput()`. Use it when one team's app calls another's, and each deploys on its own.

```pkl
["PAYMENTS_URL"] = kdst.stageOutput("payments", kdst.stage, "httpApiUrl")
```

- `kdst.stage` pairs the stages: shop on `development` reads payments on `development`, on `production` payments on `production`, and on a preview such as `pull-request-142` payments on `pull-request-142`. A fixed stage name, `"production"`, reads that stage from any of shop's.
- the output is read while planning, from payments' state in the same account and region, so payments is deployed first. Before that, a plan of shop stops with `payments/development is not deployed in this account and region: deploy it first`, and an output payments does not have stops it with `payments/development has no output ...; it has httpApiUrl`.
- shop gets the url when it deploys: deploy it again when payments' url changes.
- any [output](../outputs) can be read this way. `httpApiUrl` is one koldstart adds to every stage with http routes, so payments sets none.

A stage in another region or account amends what `kdst.stageOutput()` gives:

```pkl
(kdst.stageOutput("payments", "production", "httpApiUrl")) { region = "us-east-1" }
(kdst.stageOutput("payments", "production", "httpApiUrl")) { account = "123456789012" }
(kdst.stageOutput("payments", "production", "httpApiUrl")) { role = "arn:aws:iam::123456789012:role/koldstart-outputs" }
```

With `account`, shop's credentials read payments' state bucket, whose bucket policy must let them in; with `role`, koldstart assumes the role in that account to read it.

## trying it

```bash
(cd payments && kdst deploy --stage development)
(cd shop && kdst deploy --stage development)
URL="$(cd shop && kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl --request POST "$URL/checkout" --data '{"amount": 1250}'
```

The `curl` answers `{"order":"<id>","charge":"charge_<id>"}`, the charge from payments. Deploy shop first, on a stage payments does not have, and its plan stops with the message above. `kdst remove --stage development` in each deletes its stage, shop first.

payments' route here takes any request. [signed-routes](../signed-routes) makes it take only requests signed by shop's role.

## related

- [outputs](../outputs): what a stage's outputs are
- [signed-routes](../signed-routes): the same call, signed with signature version 4
- [stages](../stages): how `kdst.stage` pairs the two apps
