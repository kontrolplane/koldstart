interface Batch {
  Records?: { messageId: string; body: string }[];
}

const write = { debug: console.debug, info: console.info, warn: console.warn, error: console.error };

const log = (level: keyof typeof write, msg: string, fields: Record<string, unknown> = {}) =>
  write[level](JSON.stringify({ level, time: Date.now(), msg, service: "shop-worker", ...fields }));

export const handler = async (event: Batch) => {
  const records = event.Records ?? [];
  log("info", "batch received", { size: records.length });
  const failures: { itemIdentifier: string }[] = [];
  for (const record of records) {
    const job = JSON.parse(record.body) as { order?: string; kind?: string };
    if (job.kind === "refund") {
      log("warn", "refunds wait for a person", { order: job.order, messageId: record.messageId });
      failures.push({ itemIdentifier: record.messageId });
      continue;
    }
    log("info", "job done", { order: job.order, kind: job.kind ?? "fulfil" });
  }
  return { batchItemFailures: failures };
};
