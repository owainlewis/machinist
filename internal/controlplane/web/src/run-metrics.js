const zeroTime = "0001-01-01T00:00:00Z";
const activeTaskStates = new Set(["queued", "running"]);
const terminalTaskStates = new Set(["succeeded", "failed"]);

export function tasksInWindow(jobs, days, now = new Date()) {
  const since = new Date(now);
  since.setHours(0, 0, 0, 0);
  since.setDate(since.getDate() - Number(days) + 1);
  return jobs.filter((job) => {
    const createdAt = Date.parse(job.created_at);
    return validDate(job.created_at) && createdAt >= since.getTime() && createdAt <= now.getTime();
  });
}

export function taskAnalytics(jobs, days, now = new Date()) {
  const tasks = tasksInWindow(jobs, days, now);
  const terminalTasks = tasks.filter((task) => terminalTaskStates.has(task.state));
  const contributingDurations = terminalTasks.flatMap((task) => {
    const duration = taskDurationMillis(task.runs);
    return duration === undefined ? [] : [duration];
  });
  const succeededTasks = terminalTasks.filter((task) => task.state === "succeeded").length;

  return {
    tasks,
    totalTasks: tasks.length,
    successRate: terminalTasks.length ? succeededTasks / terminalTasks.length : null,
    failedTasks: terminalTasks.length - succeededTasks,
    activeTasks: tasks.filter((task) => activeTaskStates.has(task.state)).length,
    averageTaskDurationMillis: contributingDurations.length
      ? Math.round(contributingDurations.reduce((total, duration) => total + duration, 0) / contributingDurations.length)
      : null,
    contributingTasks: contributingDurations.length,
  };
}

export function completedRuns(jobs, days, now = new Date()) {
  return completedRunsForTasks(tasksInWindow(jobs, days, now));
}

export function completedRunsForTasks(tasks) {
  return tasks
    .flatMap((job) => job.runs)
    .filter((run) => validDate(run.completed_at))
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

export function formatSuccessRate(rate) {
  if (typeof rate !== "number" || !Number.isFinite(rate) || rate < 0 || rate > 1) return "Unavailable";
  return `${Math.round(rate * 1000) / 10}%`;
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

export function runModelSummary(runs) {
  const models = [];
  let hasUnspecifiedModel = false;
  for (const run of runs) {
    const model = typeof run.model === "string" ? run.model.trim() : "";
    if (!model) {
      hasUnspecifiedModel = true;
      continue;
    }
    if (!models.includes(model)) models.push(model);
  }
  if (hasUnspecifiedModel) models.push("Executor default");
  return models.length ? models.join(" · ") : "Executor default";
}

export function taskDurationMillis(runs) {
  const executedRuns = runs.filter((run) => run.state !== "skipped");
  if (!executedRuns.length || !executedRuns.every((run) => validDuration(run.duration_millis))) return undefined;
  return executedRuns.reduce((total, run) => total + run.duration_millis, 0);
}

export function formatReportingCoverage(summary) {
  if (!summary.completed) return "No completed steps";
  return `${summary.reported} of ${summary.completed} step${summary.completed === 1 ? "" : "s"}`;
}

export function formatTaskTokenUsage(summary) {
  if (summary.total === undefined) return "Not reported";
  const total = `${formatTokenUsage(summary.total)} tokens`;
  if (!summary.unavailable) return total;
  return `${total} reported · ${summary.unavailable} step${summary.unavailable === 1 ? "" : "s"} unreported`;
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
function validDuration(value) { return Number.isSafeInteger(value) && value >= 0; }
