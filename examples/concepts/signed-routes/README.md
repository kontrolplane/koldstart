# signed-routes

A route with `iam = true` takes only requests signed with signature version 4 by aws credentials whose policies allow `execute-api:Invoke` on it. API gateway checks the signature and the policy, and answers anything else with a `403` before the function runs. It is how one service calls another inside an account, or a person calls an internal route with their own credentials, with no key to hand out or rotate. The function finds who called in `requestContext.authorizer.iam`.

```pkl
events {
  (kdst.get("/stock/{sku}")) { iam = true }
  kdst.get("/health")
}
```

Another function that calls the route needs the action in its role, through `permissions`. It does not know the api's id, so the statement names the route on any http api of the account; the url itself comes from the other app's state with [`kdst.stageOutput()`](../stage-outputs):

```pkl
permissions {
  new {
    actions { "execute-api:Invoke" }
    resources { kdst.join(List("arn:", kdst.partition, ":execute-api:*:", kdst.accountId, ":*/*/GET/stock/*")) }
  }
}
```

A route that people call from a browser takes an authorizer instead: `http.authorizers` holds a `kdst.JwtAuthorizer`, which checks a token of an oidc issuer, or a `kdst.LambdaAuthorizer`, a function of the project that decides, and a route picks one with `authorizer = "<name>"`. [docs/configuration.md](../../../docs/configuration.md#the-http-api) has both.

## trying it

```bash
kdst deploy --stage development
URL="$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl "$URL/stock/blue-mug"
curl "$URL/stock/blue-mug" \
  --aws-sigv4 "aws:amz:eu-west-1:execute-api" \
  --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  --header "x-amz-security-token: $AWS_SESSION_TOKEN"
```

The first is a `403` with `{"message":"Forbidden"}`, from api gateway. The second answers `{"sku": "blue-mug", "inStock": 12, "caller": "arn:aws:sts::..."}`, with the arn of the credentials that signed it. `curl "$URL/health"` needs no signature. `kdst remove --stage development` deletes the stage.

## related

- [http-routes](../http-routes): the routes and the http api they share
- [stage-outputs](../stage-outputs): reading another app's url from its state
- [function-urls](../function-urls): `auth = "iam"` on a function's own url
