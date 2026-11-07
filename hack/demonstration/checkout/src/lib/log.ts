// log writes one line of json, as a service of the checkout would with a logger such as pino: its
// level, the time, what happened and the fields that say more of it.
const write = { debug: console.debug, info: console.info, warn: console.warn, error: console.error };

export type Level = keyof typeof write;

export const logger =
  (service: string) =>
  (level: Level, msg: string, fields: Record<string, unknown> = {}) =>
    write[level](JSON.stringify({ level, time: Date.now(), msg, service, ...fields }));
