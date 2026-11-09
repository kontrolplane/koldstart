import { ordersTable, type Orders } from "./lib/aws.ts";
import { log } from "./lib/log.ts";

/** Counts the orders stored so far, every hour on production. */
export const report = (orders: Pick<Orders, "count">) => async (): Promise<{ count: number }> => {
  const count = await orders.count();
  log.info("orders so far", { count });
  return { count };
};

export const handler = report(ordersTable());
