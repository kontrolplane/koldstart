"""Records each document put under incoming/ as a version of its product, and announces it."""

import json
import os
import time

import boto3

products = boto3.resource("dynamodb").Table(os.environ["PRODUCTS_NAME"])
changes = boto3.client("sns")


def handler(event, context):
    """Stores a version of the product each new object names, and publishes the change."""
    stored = []
    for record in event.get("Records", []):
        key = record["s3"]["object"]["key"]
        sku = key.removeprefix("incoming/").split("/")[0]
        version = int(time.time() * 1000)
        products.put_item(Item={"sku": sku, "version": version, "document": key})
        changes.publish(TopicArn=os.environ["CHANGES_ARN"], Message=json.dumps({"sku": sku, "version": version}))
        stored.append({"sku": sku, "version": version})
    return {"stored": stored}
