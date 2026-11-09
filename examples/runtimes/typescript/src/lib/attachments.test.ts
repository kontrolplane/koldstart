import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { objectKey, objectPrefix, parse } from "./attachments.ts";

describe("parse", () => {
  const examples: { name: string; body: string; error?: RegExp }[] = [
    { name: "a pdf", body: '{"fileName": "invoice.pdf", "contentType": "application/pdf"}' },
    { name: "a name with spaces", body: '{"fileName": "holiday photo.jpeg", "contentType": "image/jpeg"}' },
    { name: "text that is not json", body: "invoice.pdf", error: /not json/ },
    { name: "a list", body: "[]", error: /not an object/ },
    { name: "no name", body: '{"contentType": "application/pdf"}', error: /fileName/ },
    { name: "a name with a path", body: '{"fileName": "../invoice.pdf", "contentType": "application/pdf"}', error: /fileName/ },
    { name: "a name with a quote", body: '{"fileName": "in\\"voice.pdf", "contentType": "application/pdf"}', error: /fileName/ },
    { name: "a name with a new line", body: '{"fileName": "invoice\\n.pdf", "contentType": "application/pdf"}', error: /fileName/ },
    { name: "a name of two dots", body: '{"fileName": "..", "contentType": "application/pdf"}', error: /fileName/ },
    { name: "a type that is not taken", body: '{"fileName": "setup.exe", "contentType": "application/octet-stream"}', error: /contentType/ },
  ];
  for (const example of examples) {
    test(example.name, () => {
      const got = parse(example.body);
      if (example.error === undefined) {
        assert.ok(got.ok, got.ok ? "" : got.error);
      } else {
        assert.ok(!got.ok, "took a request it should refuse");
        assert.match(got.error, example.error);
      }
    });
  }
});

describe("objectKey", () => {
  test("a file is kept under its attachment's prefix", () => {
    assert.ok(objectKey("attachment-1", "invoice.pdf").startsWith(objectPrefix("attachment-1")));
  });
});
