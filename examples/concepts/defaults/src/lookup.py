"""Looks a customer up: a stand-in that logs at each level, to show which lines are kept."""

import logging

logger = logging.getLogger()


def handler(event, context):
    """Logs a line at debug and one at info, and answers with the function's memory and time left."""
    logger.debug("looking a customer up", extra={"customer": event.get("customer")})
    logger.info("looked a customer up", extra={"customer": event.get("customer")})
    return {
        "memoryMegabytes": int(context.memory_limit_in_mb),
        "millisecondsLeft": context.get_remaining_time_in_millis(),
    }
