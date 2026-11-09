type Fields = Record<string, unknown>;

/**
 * Writes one line of the function's log. With `logFormat = "json"`, lambda writes each line as
 * json with its time, level and request id, keeps only the levels from `applicationLogLevel` up,
 * and puts an object it is handed in the line's `message`, where logs insights can query its
 * fields.
 */
export const log = {
  debug: (message: string, fields: Fields = {}) => console.debug({ message, ...fields }),
  info: (message: string, fields: Fields = {}) => console.info({ message, ...fields }),
  warn: (message: string, fields: Fields = {}) => console.warn({ message, ...fields }),
  error: (message: string, fields: Fields = {}) => console.error({ message, ...fields }),
};

/** The parts of an error worth a line of the log, without the error's own fields. */
export const describe = (error: unknown): Fields =>
  error instanceof Error ? { error: error.name, detail: error.message, stack: error.stack } : { error: String(error) };
