export const boardColumns = [
  { id: "planning", title: "Planning", description: "Queued" },
  { id: "building", title: "Building", description: "Active or needs attention" },
  { id: "ready", title: "Ready", description: "Succeeded" },
];

const activeStates = new Set(["queued", "running"]);
const failedStates = new Set(["failed", "timed_out"]);

export function boardColumnForState(state) {
  if (state === "queued") return "planning";
  if (state === "succeeded") return "ready";
  return "building";
}

export function needsAttention(state) {
  return !activeStates.has(state) && state !== "succeeded";
}

export function filterJobs(jobs, filter) {
  return jobs.filter((job) => {
    if (filter === "active") return activeStates.has(job.state);
    if (filter === "failed") return failedStates.has(job.state);
    if (filter === "succeeded") return job.state === "succeeded";
    return true;
  });
}

export function groupJobsByBoardColumn(jobs) {
  const groups = { planning: [], building: [], ready: [] };
  for (const job of jobs) groups[boardColumnForState(job.state)].push(job);
  return groups;
}

export function jobCounts(jobs) {
  return jobs.reduce((result, job) => {
    result.all += 1;
    if (activeStates.has(job.state)) result.active += 1;
    if (failedStates.has(job.state)) result.failed += 1;
    if (job.state === "succeeded") result.succeeded += 1;
    return result;
  }, { all: 0, active: 0, failed: 0, succeeded: 0 });
}

export function currentRun(job) {
  return [...job.runs].reverse().find((run) => run.state !== "pending" && run.state !== "skipped") || job.runs[0];
}

export function stepProgress(runs) {
  const completeStates = new Set(["succeeded", "failed", "timed_out", "cancelled", "skipped"]);
  return { completed: runs.filter((run) => completeStates.has(run.state)).length, total: runs.length };
}
