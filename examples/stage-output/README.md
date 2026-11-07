# stage-output

Two apps, each with a `koldstart.pkl`, a state and stages of its own: [payments](./payments) takes charges, and [shop](./shop) reads the url of payments' http api from its state and charges through it.

```pkl
["PAYMENTS_URL"] = kdst.stageOutput("payments", kdst.stage, "httpApiUrl")
```

- `httpApiUrl` is the output of every stage with http routes, so payments sets no `outputs`; one it set would be read the same way.
- `kdst.stage` pairs the stages: shop on `dev` reads payments on `dev`, and on `prod` payments on `prod`.
- the output is read while planning, from payments' state in the same account and region, so payments is deployed first. Before that, a plan of shop stops with `payments/dev is not deployed in this account and region: deploy it first`.
- shop gets the url when it deploys: deploy it again when payments' url changes.
- payments is python, [src/api.py](./payments/src/api.py), and shop typescript, [src/checkout.ts](./shop/src/checkout.ts), both inferred from their files. Neither uses a package, so there is nothing to install.

## deploying it

```bash
(cd payments && kdst deploy)
(cd shop && kdst deploy)
```

Then, with the `httpApiUrl` of shop:

```bash
curl -X POST "$URL/checkout" -d '{"amount": 1250}'
```

It answers `{"order":"<id>","charge":"ch_<id>"}`, the charge from payments. `kdst remove` in each deletes its stage.
