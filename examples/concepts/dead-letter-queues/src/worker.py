"""Fails on a job that asks it to, so the message lands in jobs-dead-letters."""

import json


def handler(event, context):
    """Answers with the messages whose job is fail, for sqs to deliver again."""
    failures = [
        {"itemIdentifier": record["messageId"]}
        for record in event["Records"]
        if json.loads(record["body"]).get("task") == "fail"
    ]
    print(json.dumps({"message": "handled a batch", "messages": len(event["Records"]), "failed": len(failures)}))
    return {"batchItemFailures": failures}
