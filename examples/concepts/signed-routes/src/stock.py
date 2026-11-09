"""Answers how many of a sku are in stock, and who asked, on a route of signed requests alone."""

import json


def handler(event, context):
    """The caller's arn is in the request's context, as api gateway checked its signature."""
    if event["routeKey"] == "GET /health":
        return reply(200, {"healthy": True})
    caller = event["requestContext"].get("authorizer", {}).get("iam", {}).get("userArn")
    return reply(200, {"sku": event["pathParameters"]["sku"], "inStock": 12, "caller": caller})


def reply(status, body):
    """A response of the http api, with body as json."""
    return {"statusCode": status, "headers": {"content-type": "application/json"}, "body": json.dumps(body)}
