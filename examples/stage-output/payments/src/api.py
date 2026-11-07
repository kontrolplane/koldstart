"""Takes charges on POST /charges: a stand-in for a payment provider, which accepts every one."""

import base64
import json
import uuid


def handler(event, context):
    """Answers a charge of an amount in cents with its id."""
    body = event.get("body") or "{}"
    if event.get("isBase64Encoded"):
        body = base64.b64decode(body).decode()
    charge = json.loads(body)
    amount = charge.get("amount")
    if not isinstance(amount, int) or amount <= 0:
        return reply(400, {"error": 'send {"amount": <cents>}'})
    print(json.dumps({"msg": "took a charge", "amount": amount}))
    return reply(201, {"id": "ch_" + uuid.uuid4().hex, "amount": amount, "status": "succeeded"})


def reply(status, body):
    """A response of the http api, with body as json."""
    return {"statusCode": status, "headers": {"content-type": "application/json"}, "body": json.dumps(body)}
