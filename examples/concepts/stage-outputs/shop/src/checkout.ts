type Request = { body?: string; isBase64Encoded?: boolean };

const json = (statusCode: number, body: unknown) => ({
  statusCode,
  headers: { "content-type": "application/json" },
  body: JSON.stringify(body),
});

/** Places an order, charging it through payments at the url read from payments' stage. */
export const handler = async (request: Request) => {
  const body = request.isBase64Encoded ? Buffer.from(request.body ?? "", "base64").toString() : request.body;
  const { amount } = JSON.parse(body || "{}");
  const response = await fetch(`${process.env.PAYMENTS_URL}/charges`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ amount }),
    signal: AbortSignal.timeout(5000),
  });
  if (!response.ok) {
    return json(502, { error: `payments answered ${response.status}`, detail: await response.text() });
  }
  const charge = await response.json();
  return json(201, { order: crypto.randomUUID(), charge: charge.id });
};
