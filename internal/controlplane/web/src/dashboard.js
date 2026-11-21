// Helpers for the dashboard. They take the /api/v1/stats reply.
import { formatDuration } from "./jobs.js";

export const ranges = [7, 14, 30];

export function formatPercent(rate) {
  return Number.isFinite(rate) ? `${Math.round(rate * 100)}%` : "None yet";
}

export function formatMillis(milliseconds) {
  return Number.isFinite(milliseconds) ? formatDuration(milliseconds) : "None yet";
}

export function formatCount(value) {
  return new Intl.NumberFormat("en").format(value || 0);
}

// A compact token count: 950, 12.3k, 4.1M.
export function formatTokens(value) {
  const count = value || 0;
  if (count < 1000) return String(count);
  if (count < 1_000_000) return `${(count / 1000).toFixed(count < 10_000 ? 1 : 0)}k`;
  return `${(count / 1_000_000).toFixed(1)}M`;
}

// niceMax rounds a chart's top up to 1, 2 or 5 times a power of ten, so the
// gridlines land on round numbers.
export function niceMax(value) {
  if (!(value > 0)) return 1;
  const power = 10 ** Math.floor(Math.log10(value));
  for (const step of [1, 2, 5, 10]) if (step * power >= value) return step * power;
  return 10 * power;
}

export const outcomes = [
  ["succeeded", "Succeeded"],
  ["failed", "Failed"],
  ["cancelled", "Cancelled"],
];

export function runsOn(day) {
  return outcomes.reduce((total, [key]) => total + (day[key] || 0), 0);
}

// The day as a reader says it, in UTC like the API: "Nov 18".
export function dayLabel(day) {
  const date = new Date(`${day}T00:00:00Z`);
  return Number.isNaN(date.getTime()) ? day : date.toLocaleDateString("en", { month: "short", day: "numeric", timeZone: "UTC" });
}

// The share of a group's finished jobs that ended done.
export function doneRate(group) {
  const jobs = group?.jobs || {};
  const finished = (jobs.done || 0) + (jobs.failed || 0);
  return finished ? jobs.done / finished : NaN;
}

// sparkPoints places a series in a width by height box for a sparkline: the
// first value at the left edge, the last at the right, the top at the largest.
export function sparkPoints(values, width, height) {
  if (values.length < 2) return [];
  const top = Math.max(...values) || 1;
  return values.map((value, index) => [
    Math.round((index / (values.length - 1)) * width * 100) / 100,
    Math.round((height - (value / top) * height) * 100) / 100,
  ]);
}
