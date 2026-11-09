"""Answers with the stage it runs on and what that stage gave it."""

import os


def handler(event, context):
    """The stage, whether it is production, and the memory the stage sized the function with."""
    return {
        "stage": os.environ["STAGE"],
        "production": os.environ["PRODUCTION"] == "true",
        "memoryMegabytes": int(context.memory_limit_in_mb),
    }
