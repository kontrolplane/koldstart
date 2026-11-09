"""Stores a note in the table the stage's key encrypts, and reads it back."""

import os
import time
import uuid

import boto3

entries = boto3.resource("dynamodb").Table(os.environ["ENTRIES_NAME"])


def handler(event, context):
    """Stores the text of the event as a note, and answers with the note read back."""
    note = {"id": str(uuid.uuid4()), "text": event.get("text", "hello"), "storedAt": int(time.time())}
    entries.put_item(Item=note)
    stored = entries.get_item(Key={"id": note["id"]})["Item"]
    return {"note": {**stored, "storedAt": int(stored["storedAt"])}, "retentionDays": int(os.environ["RETENTION_DAYS"])}
