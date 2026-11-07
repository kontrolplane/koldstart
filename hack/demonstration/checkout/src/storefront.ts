import { logger } from "./lib/log";

interface Request {
  routeKey?: string;
  rawPath?: string;
  body?: string;
  pathParameters?: Record<string, string>;
  requestContext?: { requestId?: string; http?: { method?: string; sourceIp?: string } };
}

interface Response {
  statusCode: number;
  headers: Record<string, string>;
  body: string;
}

interface Item {
  productId: string;
  quantity: number;
}

const log = logger("storefront");

const products = [
  { id: "enamel-mug", name: "Enamel mug", price: 14.5 },
  { id: "logo-tee", name: "Logo tee", price: 29 },
  { id: "wool-beanie", name: "Wool beanie", price: 22 },
  { id: "canvas-tote", name: "Canvas tote", price: 18 },
  { id: "pin-badge", name: "Pin badge", price: 4.5 },
  { id: "sticker-pack", name: "Sticker pack", price: 6 },
];

const carts: Record<string, Item[]> = {
  "cart-8f2a91": [
    { productId: "logo-tee", quantity: 2 },
    { productId: "enamel-mug", quantity: 1 },
  ],
  "cart-3c7e04": [],
};

const orders: Record<string, { orderId: string; status: string; total: number; items: Item[] }> = {
  "order-20481": { orderId: "order-20481", status: "shipped", total: 72.5, items: [{ productId: "logo-tee", quantity: 2 }, { productId: "enamel-mug", quantity: 1 }] },
  "order-20482": { orderId: "order-20482", status: "awaiting payment", total: 9, items: [{ productId: "pin-badge", quantity: 2 }] },
};

const json = (statusCode: number, body: unknown): Response => ({
  statusCode,
  headers: { "content-type": "application/json" },
  body: JSON.stringify(body),
});

const total = (items: Item[]) =>
  items.reduce((sum, item) => sum + (products.find((p) => p.id === item.productId)?.price ?? 0) * item.quantity, 0);

export const handler = async (event: Request): Promise<Response> => {
  const route = event.routeKey ?? `${event.requestContext?.http?.method} ${event.rawPath}`;
  const path = event.pathParameters ?? {};
  log("debug", "request", { route, sourceIp: event.requestContext?.http?.sourceIp });

  switch (route) {
    case "GET /products":
      log("info", "products listed", { count: products.length });
      return json(200, { products });

    case "GET /carts/{cartId}": {
      const items = carts[path.cartId];
      if (!items) {
        log("warn", "cart not found", { cartId: path.cartId });
        return json(404, { error: "cart not found" });
      }
      log("info", "cart read", { cartId: path.cartId, items: items.length, total: total(items) });
      return json(200, { cartId: path.cartId, items, total: total(items), currency: process.env.CURRENCY });
    }

    case "POST /carts/{cartId}/items": {
      const item = JSON.parse(event.body ?? "") as Item;
      if (!products.some((p) => p.id === item.productId)) {
        log("warn", "product not in the catalogue", { cartId: path.cartId, productId: item.productId });
        return json(422, { error: `no product ${item.productId}` });
      }
      log("info", "item added to cart", { cartId: path.cartId, productId: item.productId, quantity: item.quantity });
      return json(201, { cartId: path.cartId, added: item });
    }

    case "POST /orders": {
      const { cartId } = JSON.parse(event.body ?? "{}") as { cartId?: string };
      const items = carts[cartId ?? ""] ?? [];
      if (items.length === 0) {
        log("warn", "order refused: the cart is empty", { cartId });
        return json(409, { error: "the cart is empty" });
      }
      const orderId = `order-${20483 + Math.floor(Math.random() * 900)}`;
      log("info", "order placed", { orderId, cartId, items: items.length, total: total(items), currency: process.env.CURRENCY });
      log("info", "payment requested", { orderId, queue: process.env.PAYMENT_REQUESTS_QUEUE?.split(":").pop() });
      return json(201, { orderId, status: "awaiting payment", total: total(items), currency: process.env.CURRENCY });
    }

    case "GET /orders/{orderId}": {
      const order = orders[path.orderId];
      if (!order) {
        log("warn", "order not found", { orderId: path.orderId });
        return json(404, { error: "order not found" });
      }
      log("info", "order read", { orderId: order.orderId, status: order.status });
      return json(200, order);
    }
  }
  log("warn", "no such route", { route });
  return json(404, { error: "not found" });
};
