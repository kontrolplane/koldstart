import { createHmac, timingSafeEqual } from "node:crypto";
import { logger } from "./lib/log";

interface Request {
  headers?: Record<string, string>;
  body?: string;
}

const log = logger("stripe-webhook");

// signed reports whether the stripe-signature header is the body signed with the webhook's
// secret, as stripe signs it: t=<time>,v1=<hmac of time.body>.
function signed(header: string | undefined, body: string): boolean {
  const parts = Object.fromEntries((header ?? "").split(",").map((p) => p.split("=") as [string, string]));
  if (!parts.t || !parts.v1) {
    return false;
  }
  const want = createHmac("sha256", process.env.STRIPE_WEBHOOK_SECRET ?? "").update(`${parts.t}.${body}`).digest("hex");
  return want.length === parts.v1.length && timingSafeEqual(Buffer.from(want), Buffer.from(parts.v1));
}

export const handler = async (request: Request) => {
  const body = request.body ?? "";
  if (!signed(request.headers?.["stripe-signature"], body)) {
    log("warn", "webhook refused: the signature does not match", { signature: request.headers?.["stripe-signature"] ? "present" : "missing" });
    return { statusCode: 400, body: "bad signature" };
  }
  const event = JSON.parse(body) as { id: string; type: string; data: { object: { metadata?: { orderId?: string }; amount?: number } } };
  const orderId = event.data.object.metadata?.orderId;
  log("info", "provider event received", { eventId: event.id, type: event.type, orderId });
  if (event.type === "payment_intent.succeeded") {
    log("info", "order marked as paid", { orderId, amount: (event.data.object.amount ?? 0) / 100 });
  }
  return { statusCode: 200, body: "ok" };
};
