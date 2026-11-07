# go-api

A link shortener in go, in [koldstart.pkl](./koldstart.pkl): one function, `api`, on two routes of the stage's http api, keeping the links in a table.

- `api` sets no `runtime` or `handler`: [cmd/api](./cmd/api) holds go files, so it is `provided.al2023`, built from `./cmd/api`.
- `kdst.post("/links")` stores a link and `kdst.get("/{id}")` redirects to it; the handler picks the route by its `RouteKey`.
- `link { ["links"] = "readwrite" }` lets it read and write the table, `kdst.table("id")`, and gives it the table's name as `LINKS_NAME`.
- [go.mod](./go.mod) pins `toolchain go1.27.3`, so every machine builds the same zip.

## deploying it

```bash
kdst deploy
```

Then, with the `httpApiUrl` it prints:

```bash
curl -X POST "$URL/links" -d '{"url": "https://kontrolplane.dev"}'
curl -i "$URL/<id>"
kdst logs -f api
```

The first answers with the link's `id`, and the second with a `302` to the url. `kdst remove` deletes the stage, and leaves the table in place, as it holds data.
