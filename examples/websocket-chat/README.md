# websocket-chat

A chat room on the stage's websocket api, in go, in [koldstart.pkl](./koldstart.pkl): `chat` keeps the open connections in a table and sends every message to each of them.

- `kdst.fromWebSocket("$connect")`, `"$disconnect"` and `"send"` are routes of the websocket api, all to `chat`, which picks one by its route key. A message picks `send` by its `action`.
- a function with websocket routes may post to the api's connections, and gets the url to post to as `WEBSOCKET_CALLBACK_URL`, which the handler gives the api management client. A connection that is gone is removed from the table.
- `chat` sets no `runtime` or `handler`: [cmd/chat](./cmd/chat) holds go files. [go.mod](./go.mod) pins `toolchain go1.27.3`.
- `connections` has `deletionPolicy = "delete"`, so `kdst remove` deletes it with the stage: a stage that is gone has no one connected.

## deploying it

```bash
kdst deploy
```

Then, in two terminals, connect to the `webSocketUrl` it prints, and send from one:

```bash
npx wscat -c "$(kdst outputs -o json | jq -r .webSocketUrl)"
> {"action": "send", "text": "hello"}
```

Both get `{"from":"<connection id>","text":"hello"}`. `kdst logs -f chat` shows a message that could not be sent.
