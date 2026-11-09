/** The nightly run, which the heartbeat alarm watches for. */
export const handler = async () => {
  console.info(JSON.stringify({ message: "the nightly run finished" }));
  return { finished: true };
};
