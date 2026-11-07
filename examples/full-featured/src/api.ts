import { DynamoDBClient } from "@aws-sdk/client-dynamodb";
import { DynamoDBDocumentClient, GetCommand } from "@aws-sdk/lib-dynamodb";
import { SendMessageCommand, SQSClient } from "@aws-sdk/client-sqs";
import { randomUUID } from "node:crypto";
import type { APIGatewayProxyEventV2, APIGatewayProxyResultV2 } from "aws-lambda";

const db = DynamoDBDocumentClient.from(new DynamoDBClient({}));
const sqs = new SQSClient({});

const json = (statusCode: number, body: unknown): APIGatewayProxyResultV2 => ({
  statusCode,
  headers: { "content-type": "application/json" },
  body: JSON.stringify(body),
});

export const handler = async (event: APIGatewayProxyEventV2): Promise<APIGatewayProxyResultV2> => {
  if (event.headers["x-api-key"] !== process.env.API_KEY) {
    return json(401, { error: "missing or wrong x-api-key" });
  }
  if (event.requestContext.http.method === "POST") {
    const id = randomUUID();
    const body = event.isBase64Encoded ? Buffer.from(event.body ?? "", "base64").toString() : event.body;
    const order = { id, ...JSON.parse(body || "{}") };
    await sqs.send(new SendMessageCommand({ QueueUrl: process.env.JOBS_URL, MessageBody: JSON.stringify(order) }));
    return json(202, { id });
  }
  const id = event.pathParameters?.id;
  const { Item } = await db.send(new GetCommand({ TableName: process.env.ORDERS_NAME, Key: { id } }));
  return Item ? json(200, Item) : json(404, { error: `no order ${id}` });
};
