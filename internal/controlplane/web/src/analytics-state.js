import { completedRuns } from "./run-metrics.js";

export function analyticsViewModel({ jobs, days, loaded, error, now }) {
  if (error) return { kind: "error", message: error, runs: [] };
  if (!loaded) return { kind: "loading", runs: [] };
  return { kind: "ready", runs: completedRuns(jobs, days, now) };
}
