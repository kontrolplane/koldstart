import assert from "node:assert/strict";
import { before, beforeEach, describe, mock, test } from "node:test";
import { api, sameKey } from "./api.ts";
import { BadCursor, type Page } from "./lib/aws.ts";
import type { Request } from "./lib/http.ts";
import type { Order } from "./lib/orders.ts";

const key = "4f1c2a9e8b7d6c5f";

/** A request of the http api to the route, with the stage's key unless it sets its own. */
const request = (routeKey: string, more: Partial<Request> = {}): Request =>
  ({
    version: "2.0",
    routeKey,
    rawPath: routeKey.split(" ")[1],
    rawQueryString: "",
    isBase64Encoded: false,
    ...more,
    headers: { "x-api-key": key, ...more.headers },
    requestContext: { requestId: "request-1", http: { method: routeKey.split(" ")[0] } },
  }) as unknown as Request;

const stored: Order = {
  id: "order-1",
  customerId: "customer-1",
  currency: "EUR",
  lines: [{ sku: "socks", quantity: 2 }],
  status: "received",
  createdAt: "2026-11-09T09:00:00.000Z",
};

/** The api against fakes: the orders it was handed, and the jobs it sent. */
const setup = () => {
  const sent: Order[] = [];
  const handle = api({
    orders: {
      get: async (id) => (id === stored.id ? stored : undefined),
      byCustomer: async (_customerId, limit, cursor): Promise<Page> => {
        if (cursor === "bad") {
          throw new BadCursor("the cursor is not one this api handed out");
        }
        return { orders: [stored].slice(0, limit) };
      },
      count: async () => 1,
    },
    jobs: { send: async (order) => void sent.push(order) },
    apiKey: async () => key,
    now: () => new Date("2026-11-09T10:00:00.000Z"),
  });
  return { handle, sent };
};

describe("api", () => {
  before(() => {
    for (const level of ["debug", "info", "warn", "error"] as const) {
      mock.method(console, level, () => {});
    }
  });

  let fixture: ReturnType<typeof setup>;
  beforeEach(() => {
    fixture = setup();
  });

  test("a request without the key is refused", async () => {
    const response = await fixture.handle(request("GET /orders/{id}", { headers: { "x-api-key": "wrong" }, pathParameters: { id: "order-1" } }));
    assert.equal(response.statusCode, 401);
    assert.equal(response.headers?.["content-type"], "application/problem+json");
  });

  test("an order is queued and accepted", async () => {
    const body = JSON.stringify({ customerId: "customer-1", lines: [{ sku: "socks", quantity: 2 }] });
    const response = await fixture.handle(request("POST /orders", { body }));
    assert.equal(response.statusCode, 202);
    const { id } = JSON.parse(response.body ?? "");
    assert.equal(response.headers?.location, `/orders/${id}`);
    assert.equal(fixture.sent.length, 1);
    assert.deepEqual(fixture.sent[0], { ...JSON.parse(body), currency: "EUR", id, status: "received", createdAt: "2026-11-09T10:00:00.000Z" });
  });

  test("an order sent in base64 is read", async () => {
    const body = Buffer.from(JSON.stringify({ customerId: "customer-1", lines: [{ sku: "socks", quantity: 1 }] })).toString("base64");
    const response = await fixture.handle(request("POST /orders", { body, isBase64Encoded: true }));
    assert.equal(response.statusCode, 202);
  });

  test("a retry with the idempotency key gets the first order's id", async () => {
    const body = JSON.stringify({ customerId: "customer-1", lines: [{ sku: "socks", quantity: 1 }] });
    const headers = { "idempotency-key": "checkout-7f3a91" };
    const first = await fixture.handle(request("POST /orders", { body, headers }));
    const again = await fixture.handle(request("POST /orders", { body, headers }));
    assert.equal(JSON.parse(again.body ?? "").id, JSON.parse(first.body ?? "").id);
  });

  test("an order that cannot be placed says why, and queues nothing", async () => {
    const response = await fixture.handle(request("POST /orders", { body: '{"customerId": "customer-1", "lines": []}' }));
    assert.equal(response.statusCode, 400);
    assert.match(JSON.parse(response.body ?? "").detail, /lines/);
    assert.equal(fixture.sent.length, 0);
  });

  test("an order is read by its id", async () => {
    const response = await fixture.handle(request("GET /orders/{id}", { pathParameters: { id: "order-1" } }));
    assert.equal(response.statusCode, 200);
    assert.deepEqual(JSON.parse(response.body ?? ""), stored);
  });

  test("an order that is not stored is a 404", async () => {
    const response = await fixture.handle(request("GET /orders/{id}", { pathParameters: { id: "order-2" } }));
    assert.equal(response.statusCode, 404);
  });

  test("a customer's orders come a page at a time", async () => {
    const response = await fixture.handle(
      request("GET /customers/{customerId}/orders", { pathParameters: { customerId: "customer-1" }, queryStringParameters: { limit: "10" } }),
    );
    assert.equal(response.statusCode, 200);
    assert.deepEqual(JSON.parse(response.body ?? ""), { orders: [stored] });
  });

  for (const query of [{ limit: "0" }, { limit: "101" }, { limit: "ten" }, { cursor: "bad" }]) {
    test(`a page of ${JSON.stringify(query)} is a 400`, async () => {
      const response = await fixture.handle(
        request("GET /customers/{customerId}/orders", { pathParameters: { customerId: "customer-1" }, queryStringParameters: query }),
      );
      assert.equal(response.statusCode, 400);
    });
  }

  test("a failure is a 500 that names the request, and says nothing of the cause", async () => {
    const handle = api({
      orders: { get: async () => Promise.reject(new Error("dynamodb is down")), byCustomer: async () => ({ orders: [] }), count: async () => 0 },
      jobs: { send: async () => {} },
      apiKey: async () => key,
      now: () => new Date(),
    });
    const response = await handle(request("GET /orders/{id}", { pathParameters: { id: "order-1" } }));
    assert.equal(response.statusCode, 500);
    assert.equal(response.headers?.["x-request-id"], "request-1");
    assert.doesNotMatch(response.body ?? "", /dynamodb/);
  });
});

describe("sameKey", () => {
  test("the same key matches", () => assert.ok(sameKey(key, key)));
  test("another key does not", () => assert.ok(!sameKey("x", key)));
  test("no key set matches nothing", () => assert.ok(!sameKey("", "")));
});
