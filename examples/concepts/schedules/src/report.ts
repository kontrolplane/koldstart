type Event = { kind?: string };

/** Says which schedule ran it, and when: a run by hand has no kind. */
export const handler = async (event: Event) => {
  const run = { kind: event.kind ?? "by hand", ranAt: new Date().toISOString() };
  console.info(JSON.stringify({ message: "the report ran", ...run }));
  return run;
};
