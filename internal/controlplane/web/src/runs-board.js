// One status model for every view: list groups, board columns, icons and the
// one-line reason shown under each task.

export const statusGroups = [
  { id: "needs", title: "Needs you" },
  { id: "failed", title: "Failed" },
  { id: "running", title: "Running" },
  { id: "queued", title: "Queued" },
  { id: "done", title: "Done" },
];

export const boardColumns = [
  { id: "queued", title: "Queued" },
  { id: "running", title: "Running" },
  { id: "needs", title: "Needs you" },
  { id: "done", title: "Done" },
];

const needsStates = new Set(["blocked", "awaiting_approval", "interrupted"]);
const failedStates = new Set(["failed", "timed_out"]);

export function statusGroup(state) {
  if (state === "queued") return "queued";
  if (state === "running") return "running";
  if (needsStates.has(state)) return "needs";
  if (state === "succeeded" || state === "cancelled") return "done";
  return "failed";
}

// The board has no Failed column: a failure needs you, so it sits with the other attention states.
export function boardColumnForState(state) {
  const group = statusGroup(state);
  return group === "failed" ? "needs" : group;
}

export function needsAttention(state) {
  return ["needs", "failed"].includes(statusGroup(state));
}

export function groupJobs(jobs, groupFor = (job) => statusGroup(job.state)) {
  const groups = {};
  for (const job of jobs) (groups[groupFor(job)] ||= []).push(job);
  return groups;
}

export function groupJobsByBoardColumn(jobs) {
  const groups = Object.fromEntries(boardColumns.map((column) => [column.id, []]));
  for (const job of jobs) groups[boardColumnForState(job.state)].push(job);
  return groups;
}

export function searchJobs(jobs, query) {
  const needle = query.trim().toLowerCase();
  if (!needle) return jobs;
  return jobs.filter((job) => [jobDisplayTitle(job), job.id, job.repository, job.command, job.workflow?.name]
    .some((value) => typeof value === "string" && value.toLowerCase().includes(needle)));
}

export function currentRun(job) {
  if (job.workflow) return job.runs.at(-1);
  return [...job.runs].reverse().find((run) => run.state !== "queued") || job.runs[0];
}

export function jobDisplayTitle(job) {
  const title = typeof job.github_issue_title === "string" ? job.github_issue_title.trim() : "";
  return job.task?.title || title || job.task?.spec || job.task?.source_url || job.prompt || job.id;
}

export function githubIssueReference(job) {
  const match = typeof job.trigger_subject === "string" ? job.trigger_subject.match(/\/issues\/(\d+)\/?$/) : null;
  return match ? `#${match[1]}` : "";
}

export function pullRequestURL(job) {
  for (const run of [...(job.runs || [])].reverse()) {
    const match = run.summary?.match(/https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/pull\/\d+/);
    if (match) return match[0];
  }
  return "";
}

export function runProgress(runs) {
  const completeStates = new Set(["succeeded", "failed", "timed_out", "cancelled"]);
  return { completed: runs.filter((run) => completeStates.has(run.state)).length, total: runs.length };
}

// taskReason explains, in one line, why a task is in its state and what happens next.
export function taskReason(job, workers = []) {
  const run = currentRun(job) || {};
  const step = job.workflow && job.workflow.steps?.length > 1 ? `${humanize(run.command)}: ` : "";
  switch (job.state) {
    case "queued": {
      const online = workers.filter((worker) => worker.connected);
      if (!online.length) return { text: "Waiting for a worker to come online", tone: "warning" };
      if (!online.some((worker) => (worker.repositories || []).includes(job.repository))) return { text: `No online worker has ${job.repository}`, tone: "warning" };
      return { text: `${step}Waiting for a free worker`, tone: "" };
    }
    case "running":
      return { text: `${step}Running${run.worker_name ? ` on ${run.worker_name}` : ""}`, tone: "" };
    case "awaiting_approval":
      return { text: `Waiting for your approval to start ${humanize(run.command).toLowerCase()}`, tone: "warning" };
    case "blocked":
      return { text: firstLine(run.summary) || "The agent is blocked and needs your input", tone: "warning" };
    case "interrupted":
      return { text: "The worker stopped responding. Check it stopped, then retry", tone: "warning" };
    case "timed_out":
      return { text: `${step}Timed out`, tone: "danger" };
    case "failed":
      return { text: firstLine(run.error) || firstLine(run.summary) || `${step}Failed`, tone: "danger" };
    case "cancelled":
      return { text: "Cancelled", tone: "" };
    case "succeeded": {
      const pr = pullRequestURL(job).match(/\/pull\/(\d+)$/);
      return { text: pr ? `PR #${pr[1]} opened` : firstLine(run.summary) || "Done", tone: "" };
    }
    default:
      return { text: humanize(job.state), tone: "" };
  }
}

export function humanize(name) {
  return String(name || "").replaceAll("_", " ").replaceAll("-", " ").replace(/^./, (c) => c.toUpperCase());
}

// firstLine returns the first readable sentence of agent text as plain text,
// skipping code fences, blank lines and markdown markers.
export function firstLine(value) {
  if (typeof value !== "string") return "";
  let fenced = false;
  for (const raw of value.split("\n")) {
    const line = raw.trim();
    if (line.startsWith("```")) { fenced = !fenced; continue; }
    if (fenced || !line || /^(-{3,}|\*{3,}|_{3,})$/.test(line)) continue;
    const text = line
      .replace(/^(#{1,6}\s+|>\s*|[-*+]\s+|\d+\.\s+)/, "")
      .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
      .replace(/(\*\*|__|\*|_|`)/g, "")
      .trim();
    if (text) return text.slice(0, 200);
  }
  return "";
}
