import { useMemo, useState } from "react";
import { ErrorBanner, QuietState, TopBar } from "@/components/ui/page-heading";
import { Select } from "@/components/ui/select";
import { analyticsState } from "@/analytics-state";
import { formatDurationMillis, formatReportingCoverage, formatSuccessRate, formatTokenUsage, tokenUsageSummary } from "@/run-metrics";

const windows = [{ value: "7", label: "Last 7 days" }, { value: "30", label: "Last 30 days" }];

// UsagePage shows task outcomes, timing and executor-reported tokens.
export function UsagePage({ jobs, loaded, error }) {
  const [days, setDays] = useState("30");
  const view = useMemo(() => analyticsState({ jobs, days, loaded, error }), [days, error, jobs, loaded]);
  const runs = view.runs || [];
  const usage = useMemo(() => tokenUsageSummary(runs), [runs]);
  return <>
    <TopBar title="Usage"><Select label="Time window" value={days} onValueChange={setDays} items={windows} /></TopBar>
    {error && <ErrorBanner>{error}</ErrorBanner>}
    <div className="pane-scroll">
      {view.kind === "loading" ? <QuietState title="Measuring the work" description="Loading task outcomes and reported usage." loading role="status" />
        : view.kind === "error" ? null
        : <div className="mx-auto max-w-4xl space-y-6 px-4 py-6 sm:px-6">
          <section aria-label="Task metrics" className="grid gap-px overflow-hidden rounded-lg border border-border bg-border sm:grid-cols-3">
            <Metric label="Average task time" value={formatDurationMillis(view.metrics.averageTaskDurationMillis)} note={`${view.metrics.contributingTasks} task${view.metrics.contributingTasks === 1 ? "" : "s"} with complete timing`} wide />
            <Metric label="Success rate" value={formatSuccessRate(view.metrics.successRate)} />
            <Metric label="Total tasks" value={view.metrics.totalTasks} />
            <Metric label="Active tasks" value={view.metrics.activeTasks} />
            <Metric label="Failed tasks" value={view.metrics.failedTasks} />
            <Metric label="Total reported tokens" value={formatTokenUsage(usage.total)} />
            <Metric span label="Reporting coverage" value={formatReportingCoverage(usage)} note={usage.unavailable ? `${usage.unavailable} completed run${usage.unavailable === 1 ? "" : "s"} did not report usage` : "Input plus output tokens"} />
          </section>
          <section aria-labelledby="completed-run-metrics">
            <h2 id="completed-run-metrics" className="mb-2 font-medium">Completed runs <span className="font-normal text-faint">{runs.length}</span></h2>
            <div className="overflow-x-auto rounded-lg border border-border">
              <table className="w-full min-w-[32rem] text-left">
                <thead className="bg-muted text-xs text-faint"><tr><th className="px-4 py-2 font-medium">Run</th><th className="px-4 py-2 font-medium">Command</th><th className="px-4 py-2 font-medium">Duration</th><th className="px-4 py-2 font-medium">Reported token usage</th></tr></thead>
                <tbody>{runs.length ? runs.map((run) => <tr key={run.id} className="border-t border-border"><td className="px-4 py-2 font-mono text-xs text-faint">{run.id.split("_").at(-1).slice(0, 8)}</td><td className="px-4 py-2">{run.command}</td><td className="px-4 py-2 tabular-nums">{formatDurationMillis(run.duration_millis)}</td><td className="px-4 py-2 tabular-nums">{formatTokenUsage(run.token_usage)}</td></tr>)
                  : <tr><td colSpan={4} className="px-4 py-8 text-center text-faint">No completed runs in this window.</td></tr>}</tbody>
              </table>
            </div>
          </section>
        </div>}
    </div>
  </>;
}

function Metric({ label, value, note, wide = false, span = false }) {
  return <div className={wide ? "bg-surface px-4 py-4 sm:row-span-2 sm:flex sm:flex-col sm:justify-between" : span ? "bg-surface px-4 py-3 sm:col-span-2" : "bg-surface px-4 py-3"}>
    <p className="text-xs text-faint">{label}</p>
    <p className={wide ? "mt-1 text-3xl font-semibold tracking-tight tabular-nums" : "mt-0.5 text-lg font-medium tabular-nums"}>{value}</p>
    {note && <p className="mt-1 text-xs text-faint">{note}</p>}
  </div>;
}
