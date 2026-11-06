interface Request {
  rawPath?: string;
  routeKey?: string;
  body?: string;
  pathParameters?: Record<string, string>;
  requestContext?: { requestId?: string; http?: { method?: string } };
}

interface Response {
  statusCode: number;
  headers: Record<string, string>;
  body: string;
}

interface Line {
  item: string;
  quantity: number;
}

const products = [
  { id: "mug", name: "Enamel mug", price: 12.5 },
  { id: "tee", name: "Logo tee", price: 24 },
  { id: "cap", name: "Wool cap", price: 18 },
  { id: "pin", name: "Pin badge", price: 4.5 },
];

const orders: Record<string, { id: string; status: string; total: number; lines: Line[] }> = {
  "ord_7291": { id: "ord_7291", status: "shipped", total: 37, lines: [{ item: "mug", quantity: 1 }, { item: "tee", quantity: 1 }] },
  "ord_7292": { id: "ord_7292", status: "packing", total: 9, lines: [{ item: "pin", quantity: 2 }] },
};

const write = { debug: console.debug, info: console.info, warn: console.warn, error: console.error };

const log = (level: keyof typeof write, msg: string, fields: Record<string, unknown> = {}) =>
  write[level](JSON.stringify({ level, time: Date.now(), msg, service: "shop-api", ...fields }));

const json = (statusCode: number, body: unknown): Response => ({
  statusCode,
  headers: { "content-type": "application/json" },
  body: JSON.stringify(body),
});

function price(lines: Line[]): number {
  if (lines.length === 0) {
    throw new Error("an order needs at least one line");
  }
  return lines.reduce((total, line) => {
    const product = products.find((p) => p.id === line.item);
    if (!product) {
      throw new RangeError(`no product ${line.item}`);
    }
    return total + product.price * line.quantity;
  }, 0);
}

export const handler = async (event: Request): Promise<Response> => {
  const route = event.routeKey ?? `${event.requestContext?.http?.method} ${event.rawPath}`;
  log("info", "request", { route });
  if (route === "GET /products") {
    log("debug", "listing products", { count: products.length });
    return json(200, { products });
  }
  if (route === "GET /orders/{id}") {
    const id = event.pathParameters?.id ?? "";
    const order = orders[id];
    if (!order) {
      log("warn", "no such order", { id });
      return json(404, { error: `no order ${id}` });
    }
    log("info", "order found", { id, status: order.status });
    return json(200, order);
  }
  if (route === "POST /orders") {
    const { lines = [] } = JSON.parse(event.body || "{}") as { lines?: Line[] };
    const total = price(lines);
    const id = `ord_${7293 + Math.floor(Math.random() * 600)}`;
    log("info", "order placed", { id, total, lines: lines.length, currency: process.env.CURRENCY });
    return json(201, { id, total, currency: process.env.CURRENCY });
  }
  log("warn", "no route", { route });
  return json(404, { error: `no route ${route}` });
};
