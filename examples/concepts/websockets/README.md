# websockets

`kdst.fromWebSocket()` puts a function on a route of the stage's websocket api, whose url is the output `webSocketUrl`. A client stays connected, and the api calls the function on `$connect`, on `$disconnect`, and for each message, on the route the message picks. Use it when the server has something to say before the client asks: a chat, a live board, a job's progress. [src/room.py](./src/room.py) is a chat room.

```pkl
events {
  kdst.fromWebSocket("$connect")
  kdst.fromWebSocket("$disconnect")
  kdst.fromWebSocket("send")
  kdst.fromWebSocket("$default")
}

websocket { routeSelection = "$request.body.type" }
```

A message picks its route by a field of its json, `action` unless `websocket.routeSelection` says another; one that picks none goes to `$default`. The function answers a client by posting to its connection through the api gateway management api, at the url koldstart gives it as `WEBSOCKET_CALLBACK_URL`, and koldstart lets its role post there.

The api remembers no one, so the function keeps the connections in a table. API gateway closes a connection after two hours, so each one gets an `expiresAt` two hours on, and the table's time to live deletes one whose `$disconnect` never arrived. A post to a connection that has gone is a `GoneException`, and the room forgets it then too. The table has `deletionPolicy = "delete"`: a stage that is removed leaves no one connected.

## trying it

```bash
kdst deploy --stage development
npx wscat --connect "$(kdst outputs --stage development --output json | jq --raw-output .webSocketUrl)"
> {"type": "send", "text": "hello"}
> {"type": "shout"}
```

Connect from two terminals, and both get `{"from": "<connection id>", "text": "hello"}`. The second message picks no route, and `$default` answers its sender alone with how to send. `kdst logs --stage development --function room --tail` shows the invocations. `kdst remove --stage development` deletes the stage and the table.

## related

- [link](../link): how `connections` becomes `CONNECTIONS_NAME` and the role's access to it
- [http-routes](../http-routes): request and response, on the http api
- [go](../../runtimes/go): handlers in go
