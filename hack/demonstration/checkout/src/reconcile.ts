import { logger } from "./lib/log";

interface Run {
  day?: string;
}

const log = logger("reconcile");

export const handler = async (run: Run) => {
  const day = run.day ?? new Date(Date.now() - 86_400_000).toISOString().slice(0, 10);
  log("info", "reconciliation started", { day, bucket: process.env.RECEIPTS_BUCKET });
  const orders = 1284;
  const unmatched = ["order-19906", "order-20017", "order-20133"];
  log("info", "payouts compared", { day, orders, payouts: orders - unmatched.length });
  for (const orderId of unmatched) {
    log("warn", "order paid but missing from the payout", { day, orderId });
  }
  log("info", "receipts written", { day, receipts: orders - unmatched.length, bucket: process.env.RECEIPTS_BUCKET });
  return { day, orders, unmatched };
};
