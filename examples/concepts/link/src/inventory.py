"""Answers with the variables link gave the function, to show what each access brings."""

import os

LINKED = ("STOCK_", "RESTOCK_", "REPORTS_", "LOW_STOCK_")


def handler(event, context):
    """The environment variables of the linked resources, by name."""
    return {name: value for name, value in sorted(os.environ.items()) if name.startswith(LINKED)}
