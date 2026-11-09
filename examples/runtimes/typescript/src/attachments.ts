import { randomUUID } from "node:crypto";
import { identifier, objectKey, objectPrefix, parse } from "./lib/attachments.ts";
import { json, problem, text, type Request, type Response } from "./lib/http.ts";
import { describe, log } from "./lib/log.ts";
import { s3Storage, type Storage } from "./lib/storage.ts";

/** What the function works with, handed in so the tests can give it fakes. */
export interface Dependencies {
  storage: Storage;
  newId: () => string;
  /** How long an upload or download url works, in seconds. */
  lifetimeSeconds: number;
  now: () => Date;
}

/**
 * Attachments: POST /attachments answers with a url to upload a file to, and GET
 * /attachments/{id} redirects to a url to download it from, once it is uploaded. The files go
 * straight between the client and s3, so none passes through the function.
 */
export const attachments =
  (dependencies: Dependencies) =>
  async (request: Request): Promise<Response> => {
    const requestId = request.requestContext.requestId;
    let response: Response;
    try {
      response = await route(dependencies, request);
    } catch (error) {
      log.error("answering a request", { route: request.routeKey, requestId, ...describe(error) });
      response = problem(500, "something went wrong on our side", `quote request ${requestId} when you report it`);
    }
    log.info("answered a request", { route: request.routeKey, status: response.statusCode });
    return { ...response, headers: { ...response.headers, "x-request-id": requestId } };
  };

const route = async (dependencies: Dependencies, request: Request): Promise<Response> => {
  switch (request.routeKey) {
    case "POST /attachments":
      return upload(dependencies, request);
    case "GET /attachments/{id}":
      return download(dependencies, request.pathParameters?.id ?? "");
  }
  return problem(404, `no route ${request.routeKey}`);
};

/** A new attachment: its id, and where and how to put its file. */
const upload = async (dependencies: Dependencies, request: Request): Promise<Response> => {
  const parsed = parse(text(request));
  if (!parsed.ok) {
    return problem(400, "the attachment cannot be made", parsed.error);
  }
  const id = dependencies.newId();
  const url = await dependencies.storage.uploadUrl(objectKey(id, parsed.value.fileName), parsed.value.contentType, dependencies.lifetimeSeconds);
  const expiresAt = new Date(dependencies.now().getTime() + dependencies.lifetimeSeconds * 1000).toISOString();
  log.info("made an attachment", { id, contentType: parsed.value.contentType });
  return json(
    201,
    { id, upload: { method: "PUT", url, headers: { "content-type": parsed.value.contentType }, expiresAt } },
    { location: `/attachments/${id}` },
  );
};

/** A redirect to the file of an attachment, or a 404 until it is uploaded. */
const download = async (dependencies: Dependencies, id: string): Promise<Response> => {
  if (!identifier.test(id)) {
    return problem(404, `no attachment ${id}`);
  }
  const key = await dependencies.storage.find(objectPrefix(id));
  if (key === undefined) {
    return problem(404, `no attachment ${id}`, "an attachment shows here once its file is uploaded");
  }
  const url = await dependencies.storage.downloadUrl(key, key.slice(objectPrefix(id).length), dependencies.lifetimeSeconds);
  return { statusCode: 302, headers: { location: url, "cache-control": "no-store" } };
};

export const handler = attachments({
  storage: s3Storage(),
  newId: randomUUID,
  lifetimeSeconds: 15 * 60,
  now: () => new Date(),
});
