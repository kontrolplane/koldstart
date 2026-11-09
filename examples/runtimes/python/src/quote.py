"""Answers POST /quotes with the price of sending the parcels of the request."""

import base64
import json
import logging
import os
from collections.abc import Callable

from pydantic import ValidationError

from .pricing import TARIFF, QuoteRequest, Tariff, price

logger = logging.getLogger("quote")

# The levels lambda takes that python's logging has; TRACE and FATAL are left at INFO.
LEVELS = {"DEBUG", "INFO", "WARNING", "WARN", "ERROR", "CRITICAL"}


def log_level() -> str:
    """The lowest level the function logs: applicationLogLevel, which lambda sets as AWS_LAMBDA_LOG_LEVEL."""
    level = os.environ.get("AWS_LAMBDA_LOG_LEVEL", "INFO").upper()
    return {"WARN": "WARNING"}.get(level, level) if level in LEVELS else "INFO"


def response(status: int, body: dict, content_type: str = "application/json") -> dict:
    """A response of the http api, with body as json, that no one caches."""
    return {
        "statusCode": status,
        "headers": {"content-type": content_type, "cache-control": "no-store", "x-content-type-options": "nosniff"},
        "body": json.dumps(body),
    }


def problem(status: int, title: str, detail: str | None = None, **more) -> dict:
    """A problem, as rfc 9457 describes one: what went wrong, in words the caller can act on."""
    body = {"type": "about:blank", "status": status, "title": title, **more}
    if detail is not None:
        body["detail"] = detail
    return response(status, body, "application/problem+json")


def text(event: dict) -> str:
    """The body of a request as text, decoded when api gateway handed it over in base64."""
    body = event.get("body") or ""
    return base64.b64decode(body).decode("utf-8") if event.get("isBase64Encoded") else body


def quote(event: dict, request_id: str, tariff: Tariff) -> dict:
    """The answer to one request: its quote, a 400 that lists what is wrong with it, or a 422 for
    a country the carrier does not send to."""
    try:
        request = QuoteRequest.model_validate_json(text(event) or "{}")
    except ValidationError as error:
        errors = [{"field": ".".join(str(part) for part in item["loc"]), "problem": item["msg"]} for item in error.errors()]
        logger.info("refused a quote", extra={"requestId": request_id, "errors": len(errors)})
        return problem(400, "the quote cannot be made", "fix the fields in errors", errors=errors)
    except ValueError:
        logger.info("refused a quote", extra={"requestId": request_id, "errors": 1})
        return problem(400, "the quote cannot be made", "send the request as json in utf-8")
    priced = price(request, tariff)
    if priced is None:
        return problem(422, f"no service to {request.destination}", "the carrier does not send parcels there")
    logger.info(
        "made a quote",
        extra={"requestId": request_id, "zone": priced["zone"], "parcels": len(request.parcels), "totalCents": priced["totalCents"]},
    )
    return response(200, priced)


def make_handler(tariff: Tariff) -> Callable[[dict, object], dict]:
    """The handler, pricing by tariff, so the tests can hand it a tariff of their own. A failure
    it did not expect is answered with a 500 that names the request, and logged with its cause."""

    def handler(event: dict, context: object) -> dict:
        request_id = event.get("requestContext", {}).get("requestId", "")
        try:
            answer = quote(event, request_id, tariff)
        except Exception:
            logger.exception("answering a request", extra={"requestId": request_id})
            answer = problem(500, "something went wrong on our side", f"quote request {request_id} when you report it")
        answer["headers"]["x-request-id"] = request_id
        return answer

    return handler


logger.setLevel(log_level())
handler = make_handler(TARIFF)
