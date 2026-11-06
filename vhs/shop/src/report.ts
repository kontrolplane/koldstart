interface Tick {
  day?: string;
}

const write = { debug: console.debug, info: console.info, warn: console.warn, error: console.error };

const log = (level: keyof typeof write, msg: string, fields: Record<string, unknown> = {}) =>
  write[level](JSON.stringify({ level, time: Date.now(), msg, service: "shop-report", ...fields }));

export const handler = async (event: Tick) => {
  const day = event.day ?? new Date().toISOString().slice(0, 10);
  log("info", "report started", { day });
  const report = { day, orders: 42, revenue: 1312.5, currency: "EUR", top: ["tee", "mug", "cap"] };
  log("info", "report written", report);
  return report;
};
