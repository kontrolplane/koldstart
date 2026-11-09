import type { APIGatewayProxyEventV2, APIGatewayProxyStructuredResultV2 } from "aws-lambda";

export type Request = APIGatewayProxyEventV2;
export type Response = APIGatewayProxyStructuredResultV2;

/** The headers of every response: none of them is cached, nor read as anything but its type. */
const always = { "cache-control": "no-store", "x-content-type-options": "nosniff" };

/** A response of status with body as json. */
export const json = (status: number, body: unknown, headers: Record<string, string> = {}): Response => ({
  statusCode: status,
  headers: { ...always, "content-type": "application/json", ...headers },
  body: JSON.stringify(body),
});

/** A problem, as rfc 9457 describes one: what went wrong, in words the caller can act on. */
export const problem = (status: number, title: string, detail?: string): Response => ({
  statusCode: status,
  headers: { ...always, "content-type": "application/problem+json" },
  body: JSON.stringify({ type: "about:blank", status, title, ...(detail === undefined ? {} : { detail }) }),
});

/** The body of a request as text, decoded when api gateway handed it over in base64. */
export const text = (request: Request): string =>
  request.isBase64Encoded ? Buffer.from(request.body ?? "", "base64").toString("utf8") : (request.body ?? "");

/** A header of a request. The http api hands them over in lower case. */
export const header = (request: Request, name: string): string | undefined => request.headers[name.toLowerCase()];
