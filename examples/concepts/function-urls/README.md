# function-urls

A function url is an https endpoint of one function, with no api gateway in front of it: no routes, no request limits and no authorizers, and no cost but the function's. It suits a function that answers on its own, such as a status endpoint, a webhook a provider calls, or an internal action. Each url is an output, `<function>Url` in camel case: `statusUrl` and `purgeCacheUrl` here.

```pkl
url = new {
  auth = "none"
  cors = new {
    origins { "https://status.example.com" }
    methods { "GET" }
  }
}
```

`auth` has no default, so a public url is always a choice. `"none"` takes any request: fine for a status endpoint that answers with nothing but what was deployed. Anything that reads or changes data takes `"iam"`, which lets in only requests signed with signature version 4 by a caller whose policies allow `lambda:InvokeFunctionUrl` on the function. `cors` says which sites a browser may call it from, with which methods and headers.

## trying it

```bash
kdst deploy --stage development
curl "$(kdst outputs --stage development --output json | jq --raw-output .statusUrl)"
curl "$(kdst outputs --stage development --output json | jq --raw-output .purgeCacheUrl)"
```

The first answers `{"region":"eu-west-1","function":"function-urls-development-status","version":"$LATEST"}`. The second is a `403`: its url takes only signed requests, which `curl --aws-sigv4 "aws:amz:eu-west-1:lambda" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" --header "x-amz-security-token: $AWS_SESSION_TOKEN"` sends: it answers `202` with the arn of the credentials that signed it. `kdst invoke --stage development --function status --path .koldstart/events/status/get-status.json` runs it without the url. `kdst remove --stage development` deletes the stage.

## related

- [http-routes](../http-routes): many routes on one api, with limits and cors for all of them
- [signed-routes](../signed-routes): signed requests on routes of the http api
- [container-image](../../runtimes/container-image): a status endpoint on a function url, built from a dockerfile
