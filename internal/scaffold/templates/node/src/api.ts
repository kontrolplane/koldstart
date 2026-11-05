type Request = { queryStringParameters?: Record<string, string> };

export const handler = async (event: Request) => {
  const name = event.queryStringParameters?.name ?? "world";
  return {
    statusCode: 200,
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ message: `hello ${name}` }),
  };
};
