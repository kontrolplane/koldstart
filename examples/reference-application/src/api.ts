import { createHash, timingSafeEqual } from "node:crypto";
import { BadCursor, jobsQueue, ordersTable, parameter, type Jobs, type Orders } from "./lib/aws.ts";
import { header, json, problem, text, type Request, type Response } from "./lib/http.ts";
import { describe, log } from "./lib/log.ts";
import { orderId, parse, type Order } from "./lib/orders.ts";

/** What the api works with, handed in so the tests can give it fakes. */
export interface Dependencies {
  orders: Orders;
  jobs: Jobs;
  apiKey: () => Promise<string>;
  now: () => Date;
}

/** How many of a customer's orders a page holds, unless the request asks for fewer. */
const pageSize = 25;

/** Whether two keys are the same, in a time that does not tell how much of them matched. */
export const sameKey = (given: string, want: string): boolean =>
  want !== "" &&
  timingSafeEqual(createHash("sha256").update(given).digest(), createHash("sha256").update(want).digest());

/** The api: it checks the key, picks the route by its key, and answers any failure with a 500. */
export const api =
  (dependencies: Dependencies) =>
  async (request: Request): Promise<Response> => {
    const requestId = request.requestContext.requestId;
    const started = Date.now();
    let response: Response;
    try {
      const key = await dependencies.apiKey();
      response = sameKey(header(request, "x-api-key") ?? "", key)
        ? await route(dependencies, request)
        : problem(401, "missing or wrong x-api-key", "send the stage's key, from /shop/<stage>/api-key, in x-api-key");
    } catch (error) {
      log.error("answering a request", { route: request.routeKey, requestId, ...describe(error) });
      response = problem(500, "something went wrong on our side", `quote request ${requestId} when you report it`);
    }
    log.info("answered a request", { route: request.routeKey, status: response.statusCode, milliseconds: Date.now() - started });
    return { ...response, headers: { ...response.headers, "x-request-id": requestId } };
  };

const route = async (dependencies: Dependencies, request: Request): Promise<Response> => {
  const path = request.pathParameters ?? {};
  switch (request.routeKey) {
    case "POST /orders":
      return place(dependencies, request);
    case "GET /orders/{id}": {
      const order = await dependencies.orders.get(path.id ?? "");
      return order ? json(200, order) : problem(404, `no order ${path.id}`, "an order shows here a few seconds after it is placed");
    }
    case "GET /customers/{customerId}/orders":
      return list(dependencies, path.customerId ?? "", request.queryStringParameters ?? {});
  }
  return problem(404, `no route ${request.routeKey}`);
};

/** Accepts an order and queues it for the worker to store. */
const place = async (dependencies: Dependencies, request: Request): Promise<Response> => {
  const parsed = parse(text(request));
  if (!parsed.ok) {
    return problem(400, "the order cannot be placed", parsed.error);
  }
  const key = header(request, "idempotency-key");
  if (key !== undefined && (key.length < 8 || key.length > 128)) {
    return problem(400, "the idempotency key cannot be used", "send 8 to 128 characters, unique to the order");
  }
  const order: Order = {
    ...parsed.value,
    id: orderId(parsed.value.customerId, key),
    status: "received",
    createdAt: dependencies.now().toISOString(),
  };
  await dependencies.jobs.send(order);
  log.info("accepted an order", { id: order.id, customerId: order.customerId, lines: order.lines.length });
  return json(202, { id: order.id, status: order.status }, { location: `/orders/${order.id}` });
};

/** A page of a customer's orders, the newest first. */
const list = async (dependencies: Dependencies, customerId: string, query: Record<string, string | undefined>): Promise<Response> => {
  const limit = query.limit === undefined ? pageSize : Number(query.limit);
  if (!Number.isInteger(limit) || limit < 1 || limit > 100) {
    return problem(400, "the limit cannot be used", "send a whole number from 1 to 100");
  }
  try {
    return json(200, await dependencies.orders.byCustomer(customerId, limit, query.cursor));
  } catch (error) {
    if (error instanceof BadCursor) {
      return problem(400, "the cursor cannot be used", error.message);
    }
    throw error;
  }
};

export const handler = api({
  orders: ordersTable(),
  jobs: jobsQueue(),
  apiKey: parameter("API_KEY_PARAMETER"),
  now: () => new Date(),
});
