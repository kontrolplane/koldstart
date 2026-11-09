import assert from "node:assert/strict";
import { before, describe, mock, test } from "node:test";
import { attachments } from "./attachments.ts";
import type { Request } from "./lib/http.ts";
import type { Storage } from "./lib/storage.ts";

const id = "3f2b8c1e-7a4d-4e9b-9c61-2d5f0a8b7e14";

/** A request of the http api to the route. */
const request = (routeKey: string, more: Partial<Request> = {}): Request =>
  ({
    version: "2.0",
    routeKey,
    rawPath: routeKey.split(" ")[1],
    rawQueryString: "",
    isBase64Encoded: false,
    headers: {},
    ...more,
    requestContext: { requestId: "request-1", http: { method: routeKey.split(" ")[0] } },
  }) as unknown as Request;

/** A bucket that holds the keys it is handed, and signs urls as fake.example. */
const fakeStorage = (keys: string[] = []): Storage & { asked: string[] } => {
  const asked: string[] = [];
  return {
    asked,
    uploadUrl: async (key, contentType, lifetimeSeconds) => {
      asked.push(`upload ${key} ${contentType} ${lifetimeSeconds}`);
      return `https://fake.example/${key}?upload`;
    },
    downloadUrl: async (key, fileName, lifetimeSeconds) => {
      asked.push(`download ${key} ${fileName} ${lifetimeSeconds}`);
      return `https://fake.example/${key}?download`;
    },
    find: async (prefix) => keys.find((key) => key.startsWith(prefix)),
  };
};

/** The function against storage, with a fixed id and time. */
const handle = (storage: Storage) =>
  attachments({ storage, newId: () => id, lifetimeSeconds: 900, now: () => new Date("2026-11-09T10:00:00.000Z") });

describe("attachments", () => {
  before(() => {
    for (const level of ["debug", "info", "warn", "error"] as const) {
      mock.method(console, level, () => {});
    }
  });

  test("an attachment gets a url to put its file to", async () => {
    const storage = fakeStorage();
    const body = JSON.stringify({ fileName: "invoice.pdf", contentType: "application/pdf" });
    const response = await handle(storage)(request("POST /attachments", { body }));
    assert.equal(response.statusCode, 201);
    assert.equal(response.headers?.location, `/attachments/${id}`);
    assert.deepEqual(JSON.parse(response.body ?? ""), {
      id,
      upload: {
        method: "PUT",
        url: `https://fake.example/attachments/${id}/invoice.pdf?upload`,
        headers: { "content-type": "application/pdf" },
        expiresAt: "2026-11-09T10:15:00.000Z",
      },
    });
    assert.deepEqual(storage.asked, [`upload attachments/${id}/invoice.pdf application/pdf 900`]);
  });

  test("an attachment that cannot be made says why, and signs nothing", async () => {
    const storage = fakeStorage();
    const body = JSON.stringify({ fileName: "setup.exe", contentType: "application/octet-stream" });
    const response = await handle(storage)(request("POST /attachments", { body }));
    assert.equal(response.statusCode, 400);
    assert.equal(response.headers?.["content-type"], "application/problem+json");
    assert.match(JSON.parse(response.body ?? "").detail, /contentType/);
    assert.deepEqual(storage.asked, []);
  });

  test("an uploaded attachment redirects to its file, offered under its name", async () => {
    const storage = fakeStorage([`attachments/${id}/invoice.pdf`]);
    const response = await handle(storage)(request("GET /attachments/{id}", { pathParameters: { id } }));
    assert.equal(response.statusCode, 302);
    assert.equal(response.headers?.location, `https://fake.example/attachments/${id}/invoice.pdf?download`);
    assert.deepEqual(storage.asked, [`download attachments/${id}/invoice.pdf invoice.pdf 900`]);
  });

  test("an attachment not uploaded yet is a 404", async () => {
    const response = await handle(fakeStorage())(request("GET /attachments/{id}", { pathParameters: { id } }));
    assert.equal(response.statusCode, 404);
  });

  test("an id that is not a uuid is a 404, without looking in the bucket", async () => {
    const storage = fakeStorage();
    storage.find = async () => assert.fail("looked in the bucket");
    const response = await handle(storage)(request("GET /attachments/{id}", { pathParameters: { id: "../secrets" } }));
    assert.equal(response.statusCode, 404);
  });

  test("a failure is a 500 that names the request, and says nothing of the cause", async () => {
    const storage = fakeStorage();
    storage.find = async () => Promise.reject(new Error("s3 is down"));
    const response = await handle(storage)(request("GET /attachments/{id}", { pathParameters: { id } }));
    assert.equal(response.statusCode, 500);
    assert.equal(response.headers?.["x-request-id"], "request-1");
    assert.doesNotMatch(response.body ?? "", /s3/);
  });
});
