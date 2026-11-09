"""Renders an invoice: a stand-in, there to carry its own tags."""


def handler(event, context):
    """Answers with the invoice it was asked for."""
    return {"rendered": event.get("invoice", "invoice-1")}
