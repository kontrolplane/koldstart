type Request = { queryStringParameters?: Record<string, string> };

/** Answers, or fails when asked to with ?fail=true, to set off the error alarm. */
export const handler = async (request: Request) => {
  if (request.queryStringParameters?.fail === "true") {
    throw new Error("asked to fail");
  }
  return { statusCode: 200, headers: { "content-type": "application/json" }, body: JSON.stringify({ status: "fine" }) };
};
