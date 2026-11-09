"""Stores each job of a batch once, and reports the messages it could not, for sqs to deliver again."""

import json
import os

import boto3

table = boto3.resource("dynamodb").Table(os.environ["PROCESSED_NAME"])


def handler(event, context):
    """Stores the jobs of the batch, and answers with the messages that failed."""
    failures = [
        {"itemIdentifier": record["messageId"]}
        for record in event["Records"]
        if not store(record)
    ]
    return {"batchItemFailures": failures}


def store(record):
    """Stores the job of one message unless it is stored already, and says whether it is stored now.

    The condition makes a message delivered twice, or a job sent twice, one item: sqs delivers
    each message at least once, not exactly once.
    """
    try:
        job = json.loads(record["body"])
        if job["task"] == "fail":
            raise ValueError(f"job {job['id']} asks to fail")
        table.put_item(Item={"id": job["id"], "task": job["task"]}, ConditionExpression="attribute_not_exists(id)")
        print(json.dumps({"message": "stored a job", "id": job["id"]}))
    except table.meta.client.exceptions.ConditionalCheckFailedException:
        print(json.dumps({"message": "the job was stored already", "messageId": record["messageId"]}))
    except (KeyError, ValueError) as error:
        print(json.dumps({"message": "cannot store the job", "messageId": record["messageId"], "error": str(error)}))
        return False
    return True
