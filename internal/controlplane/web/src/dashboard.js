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

// A ratio such as touches per pull request: 0, 1.5, 12.
export function formatRatio(value) {
  if (!Number.isFinite(value)) return "None yet";
  return value >= 10 || Number.isInteger(value) ? String(Math.round(value)) : value.toFixed(1);
}

export function plural(count, one, many = `${one}s`) {
  return `${formatCount(count)} ${count === 1 ? one : many}`;
}

// The kinds of human touch, in the order the dashboard names them.
export function touchParts(touches) {
  return [
    plural(touches?.replies || 0, "reply", "replies"),
    plural(touches?.reruns || 0, "rerun"),
    plural(touches?.reviews || 0, "review"),
  ].join(", ");
}

// The steps a factory's presets name. Each keeps its color whatever the range,
// so a step never changes color when another is run more.
const knownSteps = ["implement", "review", "revise", "publish"];
const stepColors = ["var(--chart-1)", "var(--chart-2)", "var(--chart-3)", "var(--chart-4)"];
const otherStep = { key: "step:other", label: "Other", color: "var(--faint)" };

// stepSeries gives each step that ran a series for the steps chart. The known
// steps keep their own color; other steps take the free colors, most run
// first, and any left over fold into Other. steps is most run first.
export function stepSeries(steps) {
  const series = [];
  const free = stepColors.filter((_, index) => !steps.includes(knownSteps[index]));
  const folded = [];
  for (const name of steps) {
    const known = knownSteps.indexOf(name);
    if (known >= 0) series.push({ key: `step:${name}`, label: label(name), color: stepColors[known], names: [name], order: known });
    else if (free.length) series.push({ key: `step:${name}`, label: label(name), color: free.shift(), names: [name], order: stepColors.length + series.length });
    else folded.push(name);
  }
  series.sort((a, b) => a.order - b.order);
  if (folded.length) series.push({ ...otherStep, names: folded });
  return series.map(({ order, ...rest }) => rest);
}

function label(name) {
  return name ? name[0].toUpperCase() + name.slice(1) : name;
}

// stepDays turns each day's step counts into the keys of stepSeries.
export function stepDays(daily, series) {
  return daily.map(day => {
    const counts = { day: day.day };
    for (const { key, names } of series) counts[key] = names.reduce((sum, name) => sum + (day.steps?.[name] || 0), 0);
    return counts;
  });
}

// linePoints places a daily series on a 0 to 100 box for a line chart. Each
// day sits at the middle of its column, so the line meets the hover columns.
export function linePoints(values, top) {
  const most = top > 0 ? top : 1;
  return values.map((value, index) => [
    Math.round(((index + 0.5) / values.length) * 10000) / 100,
    Math.round((100 - ((value || 0) / most) * 100) * 100) / 100,
  ]);
}
