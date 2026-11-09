type Request = { requestContext: { requestId: string; authorizer?: { iam?: { userArn?: string } } } };

/** Says who asked for the cache to be purged: a url of auth "iam" lets in only signed requests. */
export const handler = async (request: Request) => ({
  statusCode: 202,
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ purging: true, caller: request.requestContext.authorizer?.iam?.userArn }),
});
