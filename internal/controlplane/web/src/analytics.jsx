import { useMemo, useState } from "react";
import { Card } from "@/components/ui/card";
import { completedRuns, formatDurationMillis, formatReportingCoverage, formatTokenUsage, tokenUsageSummary } from "@/run-metrics";

export function Analytics({ jobs }) {
  const [days, setDays] = useState("30");
  const runs = useMemo(() => completedRuns(jobs, days), [days, jobs]);
  const usage = useMemo(() => tokenUsageSummary(runs), [runs]);

  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <header className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <div><h1 className="text-xl font-semibold tracking-tight">Run analytics</h1><p className="mt-1 text-sm text-muted-foreground">Measured duration and executor-reported token usage.</p></div>
      <label className="w-full sm:w-40"><span className="field-label">Time window</span><select className="field-control" value={days} onChange={(event) => setDays(event.target.value)}><option value="7">Last 7 days</option><option value="30">Last 30 days</option></select></label>
    </header>

    <div className="grid gap-3 sm:grid-cols-2">
      <Card className="min-w-0 p-4 sm:p-5"><p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Total reported tokens</p><p className="mt-2 break-all text-2xl font-semibold tabular-nums">{formatTokenUsage(usage.total)}</p><p className="mt-1 text-xs text-muted-foreground">Input plus output tokens reported in this window.</p></Card>
      <Card className="p-4 sm:p-5"><p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Reporting coverage</p><p className="mt-2 text-2xl font-semibold tabular-nums">{formatReportingCoverage(usage)}</p><p className="mt-1 text-xs text-muted-foreground">{usage.unavailable ? `${usage.unavailable} completed ${usage.unavailable === 1 ? "step has" : "steps have"} unavailable usage.` : usage.completed ? "Every completed step reported usage." : "No completed steps in this window."}</p></Card>
    </div>

    <Card className="overflow-hidden">
      <div className="hidden grid-cols-[minmax(8rem,1fr)_minmax(8rem,1fr)_10rem_12rem] gap-4 border-b border-border bg-muted/35 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground sm:grid">
        <span>Run</span><span>Agent</span><span>Duration</span><span>Reported token usage</span>
      </div>
      {runs.length ? runs.map((run) => <div key={run.id} className="grid gap-2 border-b border-border px-4 py-3.5 last:border-b-0 sm:grid-cols-[minmax(8rem,1fr)_minmax(8rem,1fr)_10rem_12rem] sm:items-center sm:gap-4">
        <p className="truncate font-mono text-xs text-muted-foreground">{shortId(run.id)}</p>
        <p className="truncate text-sm font-medium capitalize">{run.agent}</p>
        <p className="text-sm tabular-nums"><span className="sm:hidden text-muted-foreground">Duration · </span>{formatDurationMillis(run.duration_millis)}</p>
        <p className="min-w-0 break-all text-sm tabular-nums"><span className="sm:hidden text-muted-foreground">Reported tokens · </span>{formatTokenUsage(run.token_usage)}</p>
      </div>) : <div className="grid place-items-center p-12 text-sm text-muted-foreground">No completed runs in this window.</div>}
    </Card>
  </div>;
}

function shortId(id) { const [, value = id] = id.split("_", 2); return value.slice(0, 8); }
