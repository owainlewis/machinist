export function createStatusLoader({ request, apply }) {
  let latestRequest = 0;

  async function refresh() {
    const requestNumber = ++latestRequest;
    try {
      const status = await request();
      if (requestNumber !== latestRequest) return;
      apply({ kind: "success", status });
    } catch (error) {
      if (requestNumber !== latestRequest) return;
      apply({ kind: "error", message: error instanceof Error ? error.message : String(error) });
    }
  }

  return {
    refresh,
    cancel() { latestRequest += 1; },
  };
}
