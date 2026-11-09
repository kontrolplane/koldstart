"""Sends jobs to the queue: count of them, each twice when twice is true."""

import json
import os
import uuid

import boto3

queue = boto3.client("sqs")


def handler(event, context):
    """Sends the jobs the event asks for, and answers with their ids."""
    task = event.get("task", "resize")
    ids = [f"job-{uuid.uuid4()}" for _ in range(int(event.get("count", 3)))]
    for job in ids:
        for _ in range(2 if event.get("twice") else 1):
            queue.send_message(QueueUrl=os.environ["JOBS_URL"], MessageBody=json.dumps({"id": job, "task": task}))
    return {"sent": ids}
