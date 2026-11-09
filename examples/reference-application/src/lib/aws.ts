import { DynamoDBClient } from "@aws-sdk/client-dynamodb";
import { GetParameterCommand, SSMClient } from "@aws-sdk/client-ssm";
import { SendMessageCommand, SQSClient } from "@aws-sdk/client-sqs";
import { DynamoDBDocumentClient, GetCommand, QueryCommand, ScanCommand } from "@aws-sdk/lib-dynamodb";
import type { Order } from "./orders.ts";

/** A page of a customer's orders, and where the next one starts when there is one. */
export interface Page {
  orders: Order[];
  next?: string;
}

/** The orders table, as the functions use it. */
export interface Orders {
  get(id: string): Promise<Order | undefined>;
  byCustomer(customerId: string, limit: number, cursor?: string): Promise<Page>;
  count(): Promise<number>;
}

/** The queue the worker reads the orders to store from. */
export interface Jobs {
  send(order: Order): Promise<void>;
}

/** A cursor that is not one this api handed out. */
export class BadCursor extends Error {
  name = "BadCursor";
}

/** The variable link or the stage set, or an error that names it. */
const variable = (name: string): string => {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is not set: link the resource, or set it in koldstart.pkl`);
  }
  return value;
};

/** The sdk's clients, made once for each instance of the function and kept between requests. */
const dynamodb = DynamoDBDocumentClient.from(new DynamoDBClient({}));
const sqs = new SQSClient({});
const ssm = new SSMClient({});

/** The orders table link names as ORDERS_NAME, with its index of each customer's orders. */
export const ordersTable = (): Orders => ({
  async get(id) {
    const { Item } = await dynamodb.send(new GetCommand({ TableName: variable("ORDERS_NAME"), Key: { id } }));
    return Item as Order | undefined;
  },
  async byCustomer(customerId, limit, cursor) {
    let start: Record<string, unknown> | undefined;
    if (cursor !== undefined) {
      try {
        start = JSON.parse(Buffer.from(cursor, "base64url").toString("utf8"));
      } catch {
        throw new BadCursor("the cursor is not one this api handed out");
      }
    }
    const out = await dynamodb.send(
      new QueryCommand({
        TableName: variable("ORDERS_NAME"),
        IndexName: "byCustomer",
        KeyConditionExpression: "customerId = :customer",
        ExpressionAttributeValues: { ":customer": customerId },
        ScanIndexForward: false,
        Limit: limit,
        ExclusiveStartKey: start,
      }),
    );
    const next = out.LastEvaluatedKey && Buffer.from(JSON.stringify(out.LastEvaluatedKey)).toString("base64url");
    return { orders: (out.Items ?? []) as Order[], ...(next ? { next } : {}) };
  },
  async count() {
    let count = 0;
    let start: Record<string, unknown> | undefined;
    do {
      const out = await dynamodb.send(new ScanCommand({ TableName: variable("ORDERS_NAME"), Select: "COUNT", ExclusiveStartKey: start }));
      count += out.Count ?? 0;
      start = out.LastEvaluatedKey;
    } while (start);
    return count;
  },
});

/** The jobs queue link names as JOBS_URL. */
export const jobsQueue = (): Jobs => ({
  async send(order) {
    await sqs.send(new SendMessageCommand({ QueueUrl: variable("JOBS_URL"), MessageBody: JSON.stringify(order) }));
  },
});

/**
 * A secret parameter of ssm, by the arn in the variable name, read when first asked for and
 * again once its lifetime has passed, in milliseconds, so a rotated value is taken up within it
 * without a deploy.
 */
export const parameter = (name: string, lifetime = 5 * 60_000): (() => Promise<string>) => {
  let value: string | undefined;
  let expires = 0;
  return async () => {
    if (value === undefined || Date.now() >= expires) {
      const out = await ssm.send(new GetParameterCommand({ Name: variable(name), WithDecryption: true }));
      value = out.Parameter?.Value ?? "";
      expires = Date.now() + lifetime;
    }
    return value;
  };
};
