"""Deletes the invites whose expiresAt, in seconds since 1970, has passed."""

import json
import os
import time

import boto3

table = boto3.resource("dynamodb").Table(os.environ["INVITES_NAME"])


def handler(event, context):
    """Scans the invites table for expired invites and deletes them, 25 at a time."""
    scan = {
        "FilterExpression": "expiresAt < :now",
        "ExpressionAttributeValues": {":now": int(time.time())},
        "ProjectionExpression": "code",
    }
    deleted = 0
    with table.batch_writer() as batch:
        while True:
            page = table.scan(**scan)
            for item in page["Items"]:
                batch.delete_item(Key={"code": item["code"]})
                deleted += 1
            if "LastEvaluatedKey" not in page:
                break
            scan["ExclusiveStartKey"] = page["LastEvaluatedKey"]
    print(json.dumps({"msg": "deleted expired invites", "count": deleted}))
    return {"deleted": deleted}
