# full-featured

A shop's orders, in [koldstart.pkl](./koldstart.pkl): an http api in typescript that reads orders from a table and queues new ones, a worker in go that stores what is queued, and a report in typescript that counts the orders every hour, in `prod` only.

| what                  | where                                                       |
| --------------------- | ----------------------------------------------------------- |
| defaults              | `functions { default { ... } }`: memory and log retention of every function |
| http routes           | `api`: `kdst.get("/orders/{id}")` and `kdst.post("/orders")` |
| a queue               | `worker` reads `jobs`, `kdst.fromQueue("jobs")`, retrying only the orders that failed |
| a schedule            | `report`, every hour, enabled in `prod`                     |
| a table and a queue   | `kdst.table("id")` and `kdst.queue()` in `resources`, through cloud control |
| `link`                | the access and the variables, such as `ORDERS_NAME`         |
| a secret              | `API_KEY`, `kdst.param("api-key")`: `/shop/<stage>/api-key` |
| a stage of its own    | `memory` is larger in `prod`                                |
| an output             | `ordersTable`                                               |
| events to invoke with | [.koldstart/events](./.koldstart/events), for `kdst ui`     |

The worker is a go module of its own, [go.mod](./go.mod), with its `toolchain` pinned so every machine builds the same zip. The typescript is bundled by koldstart, with the aws sdk from `node_modules`.

[PklProject](./PklProject) names the schema, the package of koldstart 0.1.0, which `koldstart.pkl` imports as `@koldstart/`. Its lock file, `PklProject.deps.json`, holds the package's checksum, so it can only be written once 0.1.0 is published: until then it is not here, and the first `kdst plan` with a release writes it, to be committed. [examples_test.go](../examples_test.go) evaluates the example against the schema in this tree, `pkl/`, so it needs neither the package nor the lock file.

## deploying it

```bash
npm ci
aws ssm put-parameter --name /shop/dev/api-key --type SecureString --value "$(openssl rand -hex 16)"
kdst deploy
```

Then, with the url `deploy` prints and the key:

```bash
curl -X POST "$URL/orders" -H "x-api-key: $KEY" -d '{"item": "socks"}'
curl "$URL/orders/<id>" -H "x-api-key: $KEY"
kdst logs -f worker
```

`npm run typecheck` checks the typescript. `kdst remove` deletes the stage, and leaves the table and the queue in place, as they hold data.

The [e2e tests](../../test/e2e) deploy this example to a sandbox account on every push to main, and every night.
