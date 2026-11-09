"""Takes charges on POST /charges: a stand-in for a payment provider, which accepts every one."""

import base64
import json
import uuid


def handler(event, context):
    """Answers a charge of an amount in cents with its id, or a 400 without one."""
    body = event.get("body") or "{}"
    if event.get("isBase64Encoded"):
        body = base64.b64decode(body).decode()
    amount = json.loads(body).get("amount")
    if not isinstance(amount, int) or amount <= 0:
        return response(400, {"error": 'send {"amount": <cents>}'})
    return response(201, {"id": "charge_" + uuid.uuid4().hex, "amount": amount})


def response(status, body):
    """A response of the http api, with body as json."""
    return {"statusCode": status, "headers": {"content-type": "application/json"}, "body": json.dumps(body)}
