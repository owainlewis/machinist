export const boardColumns = [
  { id: "queued", title: "Queued", description: "Waiting to start" },
  { id: "running", title: "Running", description: "In progress" },
  { id: "finished", title: "Finished", description: "Terminal tasks" },
];

const activeStates = new Set(["queued", "running"]);
const failedStates = new Set(["failed", "timed_out"]);

export function boardColumnForState(state) {
  if (state === "queued") return "queued";
  if (state === "running") return "running";
  return "finished";
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
  const groups = { queued: [], running: [], finished: [] };
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

export function jobDisplayTitle(job) {
  const title = typeof job.github_issue_title === "string" ? job.github_issue_title.trim() : "";
  return title || job.prompt || job.id;
}

export function githubIssueReference(job) {
  const match = typeof job.trigger_subject === "string" ? job.trigger_subject.match(/\/issues\/(\d+)\/?$/) : null;
  return match ? `#${match[1]}` : "";
}

export function stepProgress(runs) {
  const completeStates = new Set(["succeeded", "failed", "timed_out", "cancelled", "skipped"]);
  return { completed: runs.filter((run) => completeStates.has(run.state)).length, total: runs.length };
}
