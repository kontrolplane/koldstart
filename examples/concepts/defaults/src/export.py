"""Exports the invoices of a month: a stand-in that logs at each level, to show which lines are kept."""

import logging

logger = logging.getLogger()


def handler(event, context):
    """Logs a line at debug and one at info, and answers with the function's memory and time left."""
    logger.debug("exporting invoices", extra={"month": event.get("month")})
    logger.info("exported invoices", extra={"month": event.get("month")})
    return {
        "memoryMegabytes": int(context.memory_limit_in_mb),
        "millisecondsLeft": context.get_remaining_time_in_millis(),
    }
