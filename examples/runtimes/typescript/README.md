# typescript

Attachments in typescript, in [koldstart.pkl](./koldstart.pkl): `POST /attachments` answers with a url to upload a file to, and `GET /attachments/{id}` redirects to a url to download it from. The files go straight between the client and s3, with urls the function signs with the aws sdk, so none passes through the function.

## how koldstart builds it

- `attachments` sets no `runtime` or `handler`: [src/attachments.ts](./src/attachments.ts) makes it `nodejs22.x`, with the handler `src/attachments.handler`, the file's `handler` export. koldstart looks for `src/<function>` with `.ts`, `.mts`, `.cts`, `.tsx`, `.js`, `.mjs`, `.cjs` or `.jsx`, in that order. Set `runtime = "nodejs24.x"` for node 24, or `handler = "src/other.handler"` for another file.
- esbuild, built into koldstart, bundles the handler and everything it imports into one file, `src/attachments.js`, for the node of the runtime, so packaging needs no node install. The aws sdk is bundled from `node_modules`, at the version in [package-lock.json](./package-lock.json), not the one lambda's node carries.
- `typecheck = true` runs `tsc --noEmit` from `node_modules` before bundling, and stops the deploy on a type error. `bundle { sourcemap = true }` puts the source map in the zip and turns on `--enable-source-maps`, so a stack trace names the lines of the typescript.
- a package with native code, such as sharp, cannot be bundled: `bundle { externals { "sharp" } }` leaves it out and copies it from `node_modules` with what it depends on, installed for lambda with `npm install --os=linux --cpu=arm64 sharp`. This example has none.
- the zip holds the same bytes for the same code on any machine, its entries dated 1980, so its hash tells koldstart whether the code changed, and a deploy of nothing new uploads nothing.
- javascript works the same, from `src/attachments.js`, without the typecheck.

The code is split as a team would keep it: [src/attachments.ts](./src/attachments.ts) routes the requests and answers failures in `application/problem+json`, [src/lib/attachments.ts](./src/lib/attachments.ts) checks what a client asks for, and [src/lib/storage.ts](./src/lib/storage.ts) is the bucket behind a small interface, which the tests replace with a fake. Each line of the log is json, at the level lambda keeps, `applicationLogLevel`.

## trying it

```bash
npm ci
kdst deploy --stage development
URL="$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl --request POST "$URL/attachments" --data '{"fileName": "invoice.pdf", "contentType": "application/pdf"}'
```

It answers `201` with the attachment's `id` and where to put the file. Put it there with the content type it was asked for, then download it:

```bash
curl --upload-file invoice.pdf --header "content-type: application/pdf" "<upload.url>"
curl --location "$URL/attachments/<id>" --output downloaded.pdf
```

The urls work for fifteen minutes. A file of another type, or a name with a path in it, is refused with a `400`. The routes take requests from anyone: [signed-routes](../../concepts/signed-routes) shows how to take only signed ones.

`kdst invoke --local --stage development --function attachments --path .koldstart/events/attachments/make-attachment.json` runs the handler on this machine against an emulator of the lambda runtime api, with the variables the stage deployed and your aws credentials, so the stage is deployed first. `kdst remove --stage development` deletes the stage and leaves the bucket in place, as it holds data.

## testing it

```bash
npm ci
npm test
npm run typecheck
```

Node's test runner runs the typescript as it is, with no build step, against a fake bucket: the routes, the checks on a request, a download before the upload, and a failure that answers `500` without saying why.

## related

- [link](../../concepts/link): how `link` gives the function the bucket's name and the access its urls need
- [resources](../../concepts/resources): `kdst.bucket()` and the other resources
- [http-routes](../../concepts/http-routes): the routes of the stage's http api
- [defaults](../../concepts/defaults): json logs and tracing for every function at once
