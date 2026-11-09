type Request = { rawPath: string; requestContext: { requestId: string; http: { method: string } } };

/**
 * Answers GET with the region it runs in and the name and version of the function, and any other
 * method with a 405.
 */
export const handler = async (request: Request) => {
  if (request.requestContext.http.method !== "GET") {
    return { statusCode: 405, headers: { allow: "GET" }, body: "" };
  }
  return {
    statusCode: 200,
    headers: { "content-type": "application/json", "cache-control": "no-store", "x-request-id": request.requestContext.requestId },
    body: JSON.stringify({
      region: process.env.AWS_REGION,
      function: process.env.AWS_LAMBDA_FUNCTION_NAME,
      version: process.env.AWS_LAMBDA_FUNCTION_VERSION,
    }),
  };
};
