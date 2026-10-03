export const stages = ["Design", "Build", "Review", "Done"];
export function groupTasks(tasks = []) {
  const g = Object.fromEntries(stages.map((s) => [s, []]));
  for (const t of tasks)
    g[
      stages.find((s) => s.toLowerCase() === String(t.stage).toLowerCase()) ||
        "Design"
    ].push(t);
  return g;
}
export function displayEvents(events = []) {
  const out = [];
  for (const e of events) {
    if (e.kind === "activity" && !e.title?.trim() && !e.text?.trim()) continue;
    const last = out.at(-1);
    if (
      e.kind === "activity" &&
      last?.kind === "activity" &&
      e.title === last.title &&
      e.text === last.text &&
      e.status === last.status
    )
      continue;
    if (
      e.kind === "message_delta" &&
      last?.kind === e.kind &&
      last.turn_id === e.turn_id
    )
      last.text += e.text || "";
    else out.push({ ...e });
  }
  return out;
}
export function isBusy(s) {
  return [
    "queued",
    "running",
    "awaiting_permission",
    "stop_requested",
  ].includes(s?.status);
}
export async function factoryRequest(path, options = {}) {
  const r = await fetch("/api/factory" + path, options);
  const b = await r.json().catch(() => ({}));
  if (!r.ok) throw Error(b.error || `Request failed (${r.status})`);
  return b;
}
