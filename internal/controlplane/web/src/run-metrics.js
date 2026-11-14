export function formatDurationMillis(milliseconds) {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "Unavailable";
  const seconds = Math.floor(milliseconds / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function formatTokens(value) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "Not reported";
  return Math.round(value).toLocaleString("en-US");
}

// A large count in a few characters, for a tile or a chart axis.
export function formatCompact(value) {
  return new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

export function formatCost(value) {
  return typeof value === "number" && Number.isFinite(value) ? `$${value.toFixed(2)}` : "Not reported";
}

function average(value) {
  return value.toFixed(1).replace(/\.0$/, "");
}

// The Go server sends a duration in nanoseconds.
export function ticketFigures(figures) {
  if (!figures) return [];
  return [
    ["Questions asked", String(figures.questions)],
    ["Feedback rounds", String(figures.feedback_rounds)],
    ["Human commits", figures.human_commits === undefined ? "Counted at merge" : String(figures.human_commits)],
    ["Tokens", formatTokens(figures.tokens)],
    ["Cost", formatCost(figures.cost)],
    ["Elapsed", formatDurationMillis(figures.elapsed / 1e6)],
    ["Merged", figures.merged ? "Yes" : "No"],
  ];
}

export function ledgerFigures(ledger) {
  if (!ledger?.merged_prs) return [];
  return [
    ["Questions", average(ledger.questions)],
    ["Feedback rounds", average(ledger.feedback_rounds)],
    ["Human commits", average(ledger.human_commits)],
    ["Tokens", formatTokens(ledger.tokens)],
    ["Cost", formatCost(ledger.cost)],
  ];
}

export function runDuration(run) {
  const started = Date.parse(run.started_at);
  const ended = Date.parse(run.ended_at);
  return Number.isFinite(started) && Number.isFinite(ended) ? formatDurationMillis(ended - started) : "";
}

// A stage passed when its process succeeded and its agent did not report
// failed or needs_input. A stage after the one that ended the run is skipped.
export function stageStatus(stage) {
  if (stage.state === "skipped") return { label: "Skipped", tone: "muted" };
  if (stage.state !== "succeeded") {
    const text = String(stage.state || "failed").replaceAll("_", " ");
    return { label: text[0].toUpperCase() + text.slice(1), tone: "bad" };
  }
  if (stage.outcome === "failed") return { label: "Reported failed", tone: "bad" };
  if (stage.outcome === "needs_input") return { label: "Asked a question", tone: "bad" };
  return { label: "Passed", tone: "good" };
}
