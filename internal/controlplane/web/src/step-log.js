import { formatDuration } from "./jobs.js";

// A step keeps the end of its output, which is where a failure shows.
export const outputLimit = 64 * 1024;

// A step begins at step.started. Runs from before the steps runner marked
// their stages with stage.started and "name=<name> kind=<kind>".
function stepName(event) {
  if (event.type === "step.started") return /^name=(.*)$/.exec(event.message || "")?.[1] || "step";
  if (event.type === "stage.started") return /^name=(.*) kind=\w+$/.exec(event.message || "")?.[1] || "stage";
  return null;
}

// steps turns a run's events into its steps, in the order they ran, with what
// each one printed. stages, from the finished run, adds each step's result.
// While the run is going, the last step that started is the one running.
export function steps(events = [], stages = [], runStatus = "") {
  const found = [];
  const decoders = new Map();
  let current = null;
  for (const event of events) {
    const name = stepName(event);
    if (name !== null) {
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
    const result = { ...step, state, duration_millis: stage.duration_millis, error: stage.error || "" };
    // Only a recorded result of a step that ran says which agents ran. A state
    // inferred while the run goes, or a skip, leaves agents unset, so the
    // pipeline shows the planned ones.
    if (stage.state && stage.state !== "skipped") result.agents = stage.agents || [];
    return result;
  });
}

// actionPrefix starts every line machinist agent-log writes for an action.
export const actionPrefix = "▸ ";

// latestAction is the last action a step's output names, without the prefix.
export function latestAction(output = "") {
  const lines = output.split("\n");
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    if (lines[index].startsWith(actionPrefix)) return lines[index].slice(actionPrefix.length).trim();
  }
  return "";
}

// pollEvents says whether a run can still send events.
export function pollEvents(run) {
  return ["queued", "running"].includes(run?.status) || (run?.status === "cancelled" && !run?.ended_at);
}

// pipeline is the job's steps as a graph shows them: every step of the preset,
// in order, with what the latest run says about each. Steps the run has not
// reached are pending while it goes, and skipped once it has ended. A run of an
// older preset shows its own steps.
//
// Each step names its agents: the ones the run recorded for it, even none, and
// otherwise the ones the preset says it runs.
export function pipeline(presetSteps = [], ran = [], runStatus = "") {
  const going = !runStatus || runStatus === "running" || runStatus === "queued";
  const preset = presetSteps.map(step => typeof step === "string" ? { name: step, agents: [] } : { name: step.name, agents: step.agents || [] });
  const planned = new Map(preset.map(step => [step.name, step.agents]));
  const seen = new Map(ran.map(step => [step.name, step]));
  const names = ran.length && ran.some(step => !planned.has(step.name)) ? ran.map(step => step.name) : preset.length ? preset.map(step => step.name) : ran.map(step => step.name);
  return names.map(name => {
    const step = seen.get(name) || { name, output: "", error: "", state: going ? "pending" : "skipped" };
    return { ...step, agents: Array.isArray(step.agents) ? step.agents : planned.get(name) || [] };
  });
}

// stepTime is how long a step took, or, while it runs, how long it has run.
export function stepTime(step, now = Date.now()) {
  if (step.state === "running" && step.started_at) return formatDuration(now - Date.parse(step.started_at));
  return step.duration_millis != null ? formatDuration(step.duration_millis) : "";
}
