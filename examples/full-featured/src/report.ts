import { DynamoDBClient, ScanCommand } from "@aws-sdk/client-dynamodb";

const db = new DynamoDBClient({});

export const handler = async () => {
  const { Count } = await db.send(new ScanCommand({ TableName: process.env.ORDERS_NAME, Select: "COUNT" }));
  console.log(JSON.stringify({ msg: "orders so far", count: Count }));
  return { count: Count };
};
