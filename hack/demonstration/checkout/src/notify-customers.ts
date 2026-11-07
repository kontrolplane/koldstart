import { logger } from "./lib/log";

interface Notification {
  Records?: { Sns: { MessageId: string; Message: string } }[];
}

const log = logger("notify-customers");

const templates: Record<string, string> = {
  "order.confirmed": "order-confirmation",
  "order.shipped": "shipping-update",
  "order.refunded": "refund-receipt",
};

export const handler = async (event: Notification) => {
  for (const record of event.Records ?? []) {
    const message = JSON.parse(record.Sns.Message) as { type: string; orderId: string; email: string };
    const template = templates[message.type];
    if (!template) {
      log("warn", "no email for this event", { type: message.type, orderId: message.orderId });
      continue;
    }
    const domain = message.email.split("@")[1];
    log("info", "email sent", { orderId: message.orderId, template, to: `•••@${domain}`, from: process.env.SENDER_ADDRESS });
  }
};
