"""Reads the price settings, publishes them to the prices bus, and counts them as a metric."""

import json
import os

import boto3
from botocore.exceptions import ClientError


def handler(event, context):
    """Answers with the settings it could read, and the ones it was refused."""
    ssm = boto3.client("ssm")
    pages = ssm.get_paginator("get_parameters_by_path").paginate(Path=os.environ["SETTINGS_PATH"], Recursive=False)
    settings = {parameter["Name"]: parameter["Value"] for page in pages for parameter in page["Parameters"]}
    refused = []
    try:
        ssm.get_parameter(Name=os.environ["SETTINGS_PATH"] + "/internal/margin")
    except ClientError as error:
        refused.append(error.response["Error"]["Code"])
    boto3.client("events").put_events(
        Entries=[{"EventBusName": os.environ["PRICES_BUS"], "Source": "pricing", "DetailType": "settings read", "Detail": json.dumps(settings)}]
    )
    boto3.client("cloudwatch").put_metric_data(
        Namespace="Pricing", MetricData=[{"MetricName": "Settings", "Value": len(settings), "Unit": "Count"}]
    )
    return {"settings": settings, "refused": refused}
