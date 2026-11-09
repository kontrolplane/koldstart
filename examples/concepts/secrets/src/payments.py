"""Shows which secrets the function has, by their length and never their value."""

import json
import os
import time

import boto3

LIFETIME_SECONDS = 300

cache = {}


def runtime_secret(variable):
    """The value of a secret read at runtime, read again once it is older than LIFETIME_SECONDS,
    so a rotated value is taken up within five minutes without a deploy."""
    value, read_at = cache.get(variable, (None, 0.0))
    if value is None or time.monotonic() - read_at > LIFETIME_SECONDS:
        name = os.environ[variable]
        if ":secretsmanager:" in name:
            value = boto3.client("secretsmanager").get_secret_value(SecretId=name)["SecretString"]
            key = os.environ.get(variable + "_KEY")
            if key:
                value = json.loads(value)[key]
        else:
            value = boto3.client("ssm").get_parameter(Name=name, WithDecryption=True)["Parameter"]["Value"]
        cache[variable] = (value, time.monotonic())
    return value


def handler(event, context):
    """The length of each secret, those read while deploying and those read now."""
    deployed = {name: len(os.environ[name]) for name in ("PROVIDER_KEY", "DATABASE_PASSWORD", "ERROR_REPORTING_URL")}
    at_runtime = {name: len(runtime_secret(name)) for name in ("WEBHOOK_SECRET", "PARTNER_TOKEN")}
    return {"readWhileDeploying": deployed, "readAtRuntime": at_runtime}
