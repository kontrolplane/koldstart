"""Archives an invoice: a stand-in, there to carry only the project's tags."""


def handler(event, context):
    """Answers with the invoice it was asked for."""
    return {"archived": event.get("invoice", "invoice-1")}
