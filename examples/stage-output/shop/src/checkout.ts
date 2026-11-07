type Request = { body?: string; isBase64Encoded?: boolean };

const json = (statusCode: number, body: unknown) => ({
  statusCode,
  headers: { "content-type": "application/json" },
  body: JSON.stringify(body),
});

export const handler = async (event: Request) => {
  const body = event.isBase64Encoded ? Buffer.from(event.body ?? "", "base64").toString() : event.body;
  const { amount } = JSON.parse(body || "{}");
  const res = await fetch(`${process.env.PAYMENTS_URL}/charges`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ amount }),
  });
  if (!res.ok) {
    return json(502, { error: `payments answered ${res.status}`, detail: await res.text() });
  }
  const charge = await res.json();
  return json(201, { order: crypto.randomUUID(), charge: charge.id });
};
