"""Fails when its event asks it to, so the run lands in nightly-failures."""

import json


def handler(event, context):
    """Runs the nightly job, or raises when the event has fail set."""
    if event.get("fail"):
        raise RuntimeError("the nightly run was asked to fail")
    print(json.dumps({"message": "the nightly run finished"}))
    return {"finished": True}
