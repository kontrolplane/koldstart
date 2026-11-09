type Request = { routeKey: string; pathParameters?: Record<string, string>; body?: string };

/** Answers with the route the http api picked and the path's parameters. */
export const handler = async (request: Request) => ({
  statusCode: request.routeKey === "$default" ? 404 : 200,
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ route: request.routeKey, parameters: request.pathParameters ?? {} }),
});
