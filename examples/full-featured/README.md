# full-featured

A shop's orders, in [koldstart.pkl](./koldstart.pkl): an http api in typescript that reads orders from a table and queues new ones, a worker in go that stores what is queued, and a report in typescript that counts the orders every hour, in `prod` only.

| what                  | where                                                       |
| --------------------- | ----------------------------------------------------------- |
| http routes           | `api`: `GET /orders/{id}` and `POST /orders`                |
| a queue               | `worker` reads `jobs`, retrying only the orders that failed |
| a schedule            | `report`, every hour, enabled in `prod`                     |
| a table and a queue   | `resources`, through cloud control                          |
| `link`                | the access and the variables, such as `ORDERS_NAME`         |
| a secret              | `API_KEY`, from ssm parameter store, for each stage         |
| a stage of its own    | `memory` is larger in `prod`                                |
| an output             | `ordersTable`                                               |
| events to invoke with | [.koldstart/events](./.koldstart/events), for `kdst ui`     |

The worker is a go module of its own, [go.mod](./go.mod), with its `toolchain` pinned so every machine builds the same zip. The typescript is bundled by koldstart, with the aws sdk from `node_modules`.

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
