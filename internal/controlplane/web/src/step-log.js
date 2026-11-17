// A step keeps the end of its output, which is where a failure shows.
export const outputLimit = 64 * 1024;

// steps turns a run's events into its steps, in the order they ran, with what
// each one printed. stages, from the finished run, adds each step's result.
// While the run is going, the last step that started is the one running.
export function steps(events = [], stages = [], runStatus = "") {
  const found = [];
  const decoders = new Map();
  let current = null;
  for (const event of events) {
    if (event.type === "step.started") {
      const name = /^name=(.*)$/.exec(event.message || "")?.[1] || "step";
      current = { name, output: "", started_at: event.time };
      found.push(current);
      decoders.clear();
      continue;
    }
    if (!current || event.type !== "process.output" || event.encoding !== "base64") continue;
    try {
      const stream = event.stream || "stdout";
      if (!decoders.has(stream)) decoders.set(stream, new TextDecoder());
      const bytes = Uint8Array.from(atob(event.data), char => char.charCodeAt(0));
      current.output = (current.output + decoders.get(stream).decode(bytes, { stream: true })).slice(-outputLimit);
    } catch { /* One malformed chunk does not hide the rest. */ }
  }
  const going = runStatus === "running" || runStatus === "queued";
  const started = new Map(found.map(step => [step.name, step]));
  const named = stages.length ? stages : found.map(step => ({ name: step.name }));
  return named.map((stage, index) => {
    const step = started.get(stage.name) || { name: stage.name, output: "" };
    let state = stage.state;
    if (!state) state = going && index === named.length - 1 ? "running" : going ? "succeeded" : "unknown";
    return { ...step, state, duration_millis: stage.duration_millis, error: stage.error || "" };
  });
}

// pollEvents says whether a run can still send events.
export function pollEvents(run) {
  return ["queued", "running"].includes(run?.status) || (run?.status === "cancelled" && !run?.ended_at);
}
