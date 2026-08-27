import { completedRuns } from "./run-metrics.js";

export function analyticsState({ jobs, days, loaded, error, now }) {
  if (error) return { kind: "error", message: error };
  if (!loaded) return { kind: "loading" };

  const runs = completedRuns(jobs, days, now);
  return runs.length ? { kind: "ready", runs } : { kind: "empty" };
}
