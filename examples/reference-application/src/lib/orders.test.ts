import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { limits, orderId, parse } from "./orders.ts";

describe("parse", () => {
  const examples: { name: string; body: string; error?: RegExp }[] = [
    { name: "an order of one line", body: '{"customerId": "customer-1", "lines": [{"sku": "socks", "quantity": 2}]}' },
    { name: "an order in pounds", body: '{"customerId": "customer-1", "currency": "GBP", "lines": [{"sku": "socks", "quantity": 1}]}' },
    { name: "text that is not json", body: "socks", error: /not json/ },
    { name: "a list", body: "[]", error: /not an object/ },
    { name: "no customer", body: '{"lines": [{"sku": "socks", "quantity": 1}]}', error: /customerId/ },
    { name: "a customer with a slash", body: '{"customerId": "customer/1", "lines": [{"sku": "socks", "quantity": 1}]}', error: /customerId/ },
    { name: "a currency the shop does not take", body: '{"customerId": "customer-1", "currency": "JPY", "lines": [{"sku": "socks", "quantity": 1}]}', error: /currency/ },
    { name: "no lines", body: '{"customerId": "customer-1", "lines": []}', error: /lines/ },
    { name: "a line without a sku", body: '{"customerId": "customer-1", "lines": [{"quantity": 1}]}', error: /lines\[0\]\.sku/ },
    { name: "half a sock", body: '{"customerId": "customer-1", "lines": [{"sku": "socks", "quantity": 0.5}]}', error: /lines\[0\]\.quantity/ },
    {
      name: "more than may be ordered at once",
      body: JSON.stringify({ customerId: "customer-1", lines: [{ sku: "socks", quantity: limits.quantity + 1 }] }),
      error: /lines\[0\]\.quantity/,
    },
  ];
  for (const example of examples) {
    test(example.name, () => {
      const got = parse(example.body);
      if (example.error === undefined) {
        assert.ok(got.ok, got.ok ? "" : got.error);
      } else {
        assert.ok(!got.ok, "parsed an order it should refuse");
        assert.match(got.error, example.error);
      }
    });
  }

  test("an order without a currency is in euros", () => {
    const got = parse('{"customerId": "customer-1", "lines": [{"sku": "socks", "quantity": 1}]}');
    assert.ok(got.ok);
    assert.equal(got.value.currency, "EUR");
  });
});

describe("orderId", () => {
  test("a retry with the same key gets the same id", () => {
    assert.equal(orderId("customer-1", "checkout-7f3a"), orderId("customer-1", "checkout-7f3a"));
  });

  test("two customers with the same key get two ids", () => {
    assert.notEqual(orderId("customer-1", "checkout-7f3a"), orderId("customer-2", "checkout-7f3a"));
  });

  test("an order without a key gets an id of its own", () => {
    assert.notEqual(orderId("customer-1"), orderId("customer-1"));
  });

  test("an id has the shape of a uuid", () => {
    assert.match(orderId("customer-1", "checkout-7f3a"), /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  });
});
