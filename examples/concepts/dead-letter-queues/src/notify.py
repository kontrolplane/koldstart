"""Fails on an announcement that says fail, so it lands in notify-dead-letters."""

import json


def handler(event, context):
    """Sends each announcement on, or raises on one whose message is fail."""
    for record in event["Records"]:
        if record["Sns"]["Message"] == "fail":
            raise RuntimeError("the announcement asked to fail")
    print(json.dumps({"message": "sent the announcements", "count": len(event["Records"])}))
    return {"sent": len(event["Records"])}
