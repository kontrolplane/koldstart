import { createHash, randomUUID } from "node:crypto";

export interface Line {
  sku: string;
  quantity: number;
}

/** An order as a customer places it. */
export interface Placed {
  customerId: string;
  currency: Currency;
  lines: Line[];
}

/** An order the api accepted and queued, as the worker stores it. */
export interface Order extends Placed {
  id: string;
  status: "received";
  createdAt: string;
}

export const currencies = ["EUR", "GBP", "USD"] as const;
export type Currency = (typeof currencies)[number];

/** What a customer may order at once, so one order cannot outgrow an item of the table. */
export const limits = { lines: 50, quantity: 100 };

export type Parsed<T> = { ok: true; value: T } | { ok: false; error: string };

/** Ids of customers and skus: what fits a path and a key, and nothing else. */
const identifier = /^[A-Za-z0-9_-]{1,64}$/;

/** Reads a placed order from json, or says what is wrong with it. */
export const parse = (body: string): Parsed<Placed> => {
  let value: unknown;
  try {
    value = JSON.parse(body);
  } catch {
    return { ok: false, error: "the body is not json" };
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return { ok: false, error: "the body is not an object" };
  }
  const { customerId, currency = "EUR", lines } = value as Record<string, unknown>;
  if (typeof customerId !== "string" || !identifier.test(customerId)) {
    return { ok: false, error: "customerId is not 1 to 64 letters, digits, - or _" };
  }
  if (typeof currency !== "string" || !currencies.includes(currency as Currency)) {
    return { ok: false, error: `currency is not one of ${currencies.join(", ")}` };
  }
  if (!Array.isArray(lines) || lines.length === 0 || lines.length > limits.lines) {
    return { ok: false, error: `lines is not a list of 1 to ${limits.lines} lines` };
  }
  const parsed: Line[] = [];
  for (const [index, line] of lines.entries()) {
    const { sku, quantity } = (line ?? {}) as Record<string, unknown>;
    if (typeof sku !== "string" || !identifier.test(sku)) {
      return { ok: false, error: `lines[${index}].sku is not 1 to 64 letters, digits, - or _` };
    }
    if (!Number.isInteger(quantity) || (quantity as number) < 1 || (quantity as number) > limits.quantity) {
      return { ok: false, error: `lines[${index}].quantity is not a whole number from 1 to ${limits.quantity}` };
    }
    parsed.push({ sku, quantity: quantity as number });
  }
  return { ok: true, value: { customerId, currency: currency as Currency, lines: parsed } };
};

/**
 * The id of a new order. With an idempotency key it is derived from the key and the customer,
 * so a client that retries a request gets the id of the first, and the worker stores the order
 * once.
 */
export const orderId = (customerId: string, idempotencyKey?: string): string => {
  if (idempotencyKey === undefined) {
    return randomUUID();
  }
  const digest = createHash("sha256").update(`${customerId}\n${idempotencyKey}`).digest("hex");
  return `${digest.slice(0, 8)}-${digest.slice(8, 12)}-${digest.slice(12, 16)}-${digest.slice(16, 20)}-${digest.slice(20, 32)}`;
};
