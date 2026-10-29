import { taskPresentation } from "./task-presentation.js";
import { analyticsState } from "./analytics-state.js";
import { tokenUsageSummary, estimatedCostSummary } from "./run-metrics.js";

import { dashboardAttention } from "./runs-board.js";
const stamp = job => Date.parse(job.updated_at || job.created_at) || 0;
const latest = jobs => [...jobs].sort((a, b) => stamp(b) - stamp(a));

export function dashboardState({ jobs = [], workers = [], automations, days = "7", loaded, error, now = new Date() }) {
  const base = analyticsState({ jobs, days, loaded, error, now });
  if (base.kind === "loading" || base.kind === "error") return base;
  const periodJobs = base.metrics.tasks;
  const active = latest(jobs.filter(job => ["running", "queued"].includes(job.state)));
  const ready = latest(jobs.filter(job => job.state === "awaiting_merge"));
  const attention = latest(jobs.filter(dashboardAttention));
  const counts = { succeeded: 0, failed: 0, cancelled: 0, unfinished: 0 };
  for (const job of periodJobs) {
    if (job.state === "succeeded") counts.succeeded++;
    else if (["failed", "timed_out"].includes(job.state)) counts.failed++;
    else if (job.state === "cancelled") counts.cancelled++;
    else counts.unfinished++;
  }
  // Calendar days in the viewer's timezone, including DST transitions. Never
  // bucket by UTC then label those dates as local days.
  const dateKey = date => `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
  const dates = Array.from({ length: Number(days) }, (_, i) => {
    const date = new Date(now);
    date.setHours(0, 0, 0, 0);
    date.setDate(date.getDate() - Number(days) + i + 1);
    return { key: dateKey(date), date, count: 0 };
  });
  const byDate = new Map(dates.map(day => [day.key, day]));
  for (const job of periodJobs) {
    const day = byDate.get(dateKey(new Date(job.created_at)));
    if (day) day.count++;
  }
  const timedRuns = base.runs.filter(run => Number.isSafeInteger(run.duration_millis) && run.duration_millis >= 0);
  return {
    ...base, active, ready, attention, dates, counts,
    running: active.filter(job => job.state === "running").length,
    queued: active.filter(job => job.state === "queued").length,
    recent: latest(periodJobs).slice(0, 6),
    timedMillis: timedRuns.length ? timedRuns.reduce((total, run) => total + run.duration_millis, 0) : null,
    timedRuns: timedRuns.length,
    usage: tokenUsageSummary(base.runs),
    cost: estimatedCostSummary(periodJobs),
    onlineWorkers: workers.filter(worker => worker.connected).length,
    totalWorkers: workers.length,
    scheduled: Array.isArray(automations) ? automations.filter(a => a.cron && !a.paused).length : null,
    automationCount: Array.isArray(automations) ? automations.length : null,
  };
}

const noProject = "__no_project__";
export function workQueueState({ jobs = [], repositories = [], project = "", loaded, error }) {
  const names = new Set([...repositories, ...jobs.map(job => job.repository).filter(Boolean)]);
  if (project && project !== noProject) names.add(project);
  const projects = [...names].sort().map(name => ({ value: name, label: repositories.includes(name) ? name : `${name} (unavailable)` }));
  if (jobs.some(job => !job.repository) || project === noProject) projects.push({ value: noProject, label: "No project recorded" });
  if (error) return { kind: "error", message: error, projects };
  if (!loaded) return { kind: "loading", projects };
  const selected = latest(jobs.filter(job => !project || (job.repository || noProject) === project));
  const attention = [], active = [], ready = [], outcomes = [];
  for (const job of selected) {
    // Holds always require a private decision, including on otherwise finished work.
    if (job.publication_hold || dashboardAttention(job)) attention.push(job);
    else if (["running", "queued"].includes(job.state)) active.push(job);
    else if (job.state === "awaiting_merge") ready.push(job);
    else if (["succeeded", "failed", "timed_out", "cancelled", "interrupted"].includes(job.state)) outcomes.push(job);
  }
  return { kind: "ready", projects, attention, active, ready, recent: outcomes.slice(0, 6), empty: selected.length === 0 };
}

function shortEvidence(value) {
  if (typeof value !== "string") return "";
  const text = value.trim();
  // Structured output and code belong in the report reader, never in queue rows.
  if (!text || /^[{\["`]/.test(text)) return "";
  const line = text.split(/\r?\n/)[0].replace(/^#+\s*/, "").trim();
  return line.length > 160 ? `${line.slice(0, 157)}…` : line;
}

export function queueSummary(job) {
  if (job.publication_hold) return "Publication held private. Review the hold before publishing.";
  if (job.state === "queued" && job.waiting_reasons?.length) {
    const reasons = new Set(job.waiting_reasons);
    for (const [reason, message] of [
      ["cleanup_unconfirmed", "Waiting for previous agent processes to be confirmed stopped."],
      ["global_capacity", "Waiting for an available factory slot."],
      ["repository_capacity", "Waiting for this project's active job to finish."],
      ["awaiting_runner", "Waiting for a compatible runner."],
      ["runner_busy", "Waiting for the runner to finish its current job."],
      ["awaiting_dispatch", "Ready; waiting for dispatch."],
    ]) if (reasons.has(reason)) return message;
    if (reasons.has("credential_serialization_possible")) return "Queued for dispatch. This Codex credential stream runs one job at a time.";
  }
  const { result: run } = taskPresentation(job);
  const recordedError = typeof run?.error === "string" ? run.error : "";
  if (/json: cannot unmarshal/i.test(recordedError)) return "The agent returned a result Machinist could not read. Open the run for details.";
  if (/Google Cloud login has expired or is unavailable/i.test(recordedError)) return "Cloud sign-in was unavailable for this attempt. Check the runner connection.";
  const evidence = shortEvidence(recordedError) || shortEvidence(run?.summary);
  if (evidence) return evidence;
  if (job.state === "running") {
    const phase = { starting_sandbox: "Preparing the isolated workspace.", running_agent: "Agent working.", publishing: "Publishing the task result.", completing: "Recording the task result." }[run?.phase];
    if (phase) return phase;
  }
  return ({ queued: "Queued; waiting to start.", running: "Work is running; no summary recorded yet.", awaiting_approval: "Approval required.", awaiting_merge: "Waiting for review.", blocked: "Blocked; no explanation recorded.", interrupted: "Work interrupted.", failed: "Work failed; no explanation recorded.", timed_out: "Work timed out.", cancelled: "Work cancelled.", succeeded: "Work completed; no summary recorded." })[job.state] || "No summary recorded.";
}
