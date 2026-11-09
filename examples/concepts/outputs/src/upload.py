"""Stores the body of POST /uploads in the uploads bucket, under a new key."""

import os
import uuid

import boto3

bucket = os.environ["UPLOADS_NAME"]
s3 = boto3.client("s3")


def handler(event, context):
    """Puts the body in the bucket, and answers with its key."""
    key = "uploads/" + uuid.uuid4().hex
    s3.put_object(Bucket=bucket, Key=key, Body=(event.get("body") or "").encode())
    return {"statusCode": 201, "headers": {"content-type": "application/json"}, "body": '{"key": "' + key + '"}'}
