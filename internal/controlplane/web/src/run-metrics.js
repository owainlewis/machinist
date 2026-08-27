const zeroTime = "0001-01-01T00:00:00Z";

export function completedRuns(jobs, days, now = new Date()) {
  const since = new Date(now);
  since.setHours(0, 0, 0, 0);
  since.setDate(since.getDate() - Number(days) + 1);
  return jobs
    .flatMap((job) => job.runs)
    .filter((run) => validDate(run.completed_at) && Date.parse(run.completed_at) >= since.getTime())
    .sort((left, right) => Date.parse(right.completed_at) - Date.parse(left.completed_at));
}

export function formatDurationMillis(milliseconds) {
  if (!Number.isSafeInteger(milliseconds) || milliseconds < 0) return "Unavailable";
  if (milliseconds < 1000) return `${milliseconds}ms`;
  const seconds = Math.floor(milliseconds / 1000);
  const remainder = milliseconds % 1000;
  if (seconds < 60) return remainder ? `${seconds}.${String(remainder).padStart(3, "0").replace(/0+$/, "")}s` : `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) return `${minutes}m ${remainingSeconds}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m ${remainingSeconds}s`;
}

export function formatTokenUsage(value) {
  return typeof value === "string" && /^(0|[1-9]\d*)$/.test(value) ? value.replace(/\B(?=(\d{3})+(?!\d))/g, ",") : "Unavailable";
}

export function tokenUsageSummary(runs) {
  let total = 0n;
  let reported = 0;
  let completed = 0;
  for (const run of runs) {
    if (!validDate(run.completed_at)) continue;
    completed += 1;
    if (!validTokenUsage(run.token_usage)) continue;
    total += BigInt(run.token_usage);
    reported += 1;
  }
  return {
    total: reported ? total.toString() : undefined,
    reported,
    completed,
    unavailable: completed - reported,
  };
}

export function formatReportingCoverage(summary) {
  if (!summary.completed) return "No completed steps";
  return `${summary.reported} of ${summary.completed} step${summary.completed === 1 ? "" : "s"}`;
}

export function runDetails(run) {
  const values = [run.executor];
  if (run.worker_name) values.push(run.worker_name);
  if (run.model) values.push(run.model);
  if (Number.isSafeInteger(run.duration_millis)) values.push(formatDurationMillis(run.duration_millis));
  if (validDate(run.completed_at)) values.push(validTokenUsage(run.token_usage) ? `${formatTokenUsage(run.token_usage)} tokens` : "Token usage unavailable");
  return values.join(" · ");
}

function validTokenUsage(value) { return typeof value === "string" && /^(0|[1-9]\d*)$/.test(value); }
function validDate(value) { return Boolean(value && value !== zeroTime && Number.isFinite(Date.parse(value))); }
