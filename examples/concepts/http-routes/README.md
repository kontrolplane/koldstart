# http-routes

Every `kdst.get()`, `kdst.post()`, `kdst.put()`, `kdst.patch()`, `kdst.del()` and `kdst.any()` of a stage is a route of one http api, which koldstart makes for the stage, and whose url is the output `httpApiUrl`. A path takes parameters, as `/books/{id}`, and `$default` takes every request no other route does. One function can take many routes and pick one by the event's `routeKey`, as [src/books.ts](./src/books.ts) does, or each route can have a function of its own.

```pkl
events {
  kdst.get("/books/{id}")
  (kdst.post("/books")) { throttling = new { rate = 5; burst = 10 } }
  kdst.any("$default")
}
```

`http` sets what the routes share: a request limit for the stage, which a route's own `throttling` takes the place of, a line in an access log for every request, kept in `/aws/http-api/<app>-<stage>`, and the origins, methods and headers a browser may use across origins. `http.domain` and `http.certificate`, an acm certificate in the api's region, put the api on a domain of your own; the output `httpDomainTarget` is then where its dns record points.

`kdst.del()` is `DELETE`: `delete` is a keyword of pkl.

## trying it

```bash
kdst deploy --stage development
URL="$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl "$URL/books/the-hobbit"
curl --request POST "$URL/books" --data '{"title": "The Hobbit"}'
curl "$URL/nowhere"
```

They answer `{"route":"GET /books/{id}","parameters":{"id":"the-hobbit"}}`, `{"route":"POST /books",...}`, and a `404` from `$default`. `kdst offline --stage development` serves the same routes on `127.0.0.1:3000`, or `--port`, each request run on this machine, without the limits or authorizers. `kdst remove --stage development` deletes the stage.

## related

- [signed-routes](../signed-routes): routes that take only requests signed with aws credentials
- [function-urls](../function-urls): a function on a url of its own, without an api
- [outputs](../outputs): `httpApiUrl` and the other outputs a stage has
