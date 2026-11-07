import { logger } from "./lib/log";

interface Batch {
  Records?: { messageId: string; body: string }[];
}

interface PaymentRequest {
  orderId: string;
  amount: number;
  currency: string;
  card: string;
}

const log = logger("payments");

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

// charge asks the payment provider to capture the amount. A card ending in 0002 is declined, as
// stripe's test cards are, and one ending in 3184 makes the provider take longer than the
// function may run.
async function charge(request: PaymentRequest): Promise<string> {
  if (request.card.endsWith("0002")) {
    throw Object.assign(new Error("Your card was declined."), { code: "card_declined" });
  }
  await sleep(request.card.endsWith("3184") ? 8000 : 40 + Math.random() * 120);
  return `ch_${Math.random().toString(36).slice(2, 16)}`;
}

export const handler = async (event: Batch) => {
  const records = event.Records ?? [];
  log("info", "batch received", { size: records.length, provider: process.env.PAYMENT_PROVIDER });
  const failures: { itemIdentifier: string }[] = [];
  for (const record of records) {
    const request = JSON.parse(record.body) as PaymentRequest;
    try {
      const chargeId = await charge(request);
      log("info", "payment captured", { orderId: request.orderId, amount: request.amount, currency: request.currency, chargeId });
    } catch (err) {
      const e = err as Error & { code?: string };
      log("warn", "payment failed", { orderId: request.orderId, code: e.code, reason: e.message, retry: false });
      failures.push({ itemIdentifier: record.messageId });
    }
  }
  return { batchItemFailures: failures };
};
