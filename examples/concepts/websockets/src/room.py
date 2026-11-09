"""A chat room on the stage's websocket api.

It keeps the open connections in the connections table, and sends each message to all of them.
"""

import json
import os
import time

import boto3

table = boto3.resource("dynamodb").Table(os.environ["CONNECTIONS_NAME"])
management = boto3.client("apigatewaymanagementapi", endpoint_url=os.environ["WEBSOCKET_CALLBACK_URL"])

lifetime = 2 * 60 * 60


def handler(event, context):
    """Keeps a connection on $connect, forgets it on $disconnect, and sends a send to everyone."""
    route = event["requestContext"]["routeKey"]
    connection = event["requestContext"]["connectionId"]
    if route == "$connect":
        table.put_item(Item={"id": connection, "expiresAt": int(time.time()) + lifetime})
    elif route == "$disconnect":
        table.delete_item(Key={"id": connection})
    elif route == "send":
        text = json.loads(event.get("body") or "{}").get("text", "")
        broadcast(json.dumps({"from": connection, "text": text}).encode())
    else:
        post(connection, json.dumps({"error": 'send {"type": "send", "text": "..."}'}).encode())
    return {"statusCode": 200}


def broadcast(data):
    """Posts data to every connection that has not expired."""
    scan = {"FilterExpression": "expiresAt > :now", "ExpressionAttributeValues": {":now": int(time.time())}}
    while True:
        page = table.scan(**scan)
        for item in page["Items"]:
            post(item["id"], data)
        if "LastEvaluatedKey" not in page:
            return
        scan["ExclusiveStartKey"] = page["LastEvaluatedKey"]


def post(connection, data):
    """Posts data to one connection, and forgets it when it has gone."""
    try:
        management.post_to_connection(ConnectionId=connection, Data=data)
    except management.exceptions.GoneException:
        table.delete_item(Key={"id": connection})
