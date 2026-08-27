import { useMemo, useState } from "react";
import { Card } from "@/components/ui/card";
import { completedRunsForTasks, formatDurationMillis, formatSuccessRate, formatTokenUsage, taskAnalytics } from "@/run-metrics";

export function Analytics({ jobs }) {
  const [days, setDays] = useState("30");
  const metrics = useMemo(() => taskAnalytics(jobs, days), [days, jobs]);
  const runs = useMemo(() => completedRunsForTasks(metrics.tasks), [metrics.tasks]);

  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <header className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <div><h1 className="text-xl font-semibold tracking-tight">Task analytics</h1><p className="mt-1 text-sm text-muted-foreground">Task outcomes with measured run duration and executor-reported token usage.</p></div>
      <label className="w-full sm:w-40"><span className="field-label">Time window</span><select className="field-control" value={days} onChange={(event) => setDays(event.target.value)}><option value="7">Last 7 days</option><option value="30">Last 30 days</option></select></label>
    </header>

    <section className="grid gap-4 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]" aria-label="Task metrics">
      <Card className="flex min-h-48 flex-col justify-between border-primary/25 bg-primary/5 p-5 sm:p-6">
        <div><p className="text-sm font-medium text-muted-foreground">Average task time</p><p className="mt-3 break-words text-4xl font-semibold tracking-tight tabular-nums sm:text-5xl">{formatDurationMillis(metrics.averageTaskDurationMillis)}</p></div>
        <p className="mt-6 text-sm text-muted-foreground">{metrics.contributingTasks} contributing task{metrics.contributingTasks === 1 ? "" : "s"} with complete run timing</p>
      </Card>
      <div className="grid grid-cols-2 gap-3 sm:gap-4">
        <Metric label="Total tasks" value={metrics.totalTasks} />
        <Metric label="Success rate" value={formatSuccessRate(metrics.successRate)} />
        <Metric label="Failed tasks" value={metrics.failedTasks} />
        <Metric label="Active tasks" value={metrics.activeTasks} />
      </div>
    </section>

    <section aria-labelledby="completed-run-metrics">
      <div className="mb-3"><h2 id="completed-run-metrics" className="text-sm font-semibold">Completed run metrics</h2><p className="mt-1 text-xs text-muted-foreground">Duration and reported token usage for runs belonging to tasks in this window.</p></div>
    <Card className="overflow-hidden">
      <div className="hidden grid-cols-[minmax(8rem,1fr)_minmax(8rem,1fr)_10rem_12rem] gap-4 border-b border-border bg-muted/35 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground sm:grid">
        <span>Run</span><span>Agent</span><span>Duration</span><span>Reported token usage</span>
      </div>
      {runs.length ? runs.map((run) => <div key={run.id} className="grid gap-2 border-b border-border px-4 py-3.5 last:border-b-0 sm:grid-cols-[minmax(8rem,1fr)_minmax(8rem,1fr)_10rem_12rem] sm:items-center sm:gap-4">
        <p className="truncate font-mono text-xs text-muted-foreground">{shortId(run.id)}</p>
        <p className="truncate text-sm font-medium capitalize">{run.agent}</p>
        <p className="text-sm tabular-nums"><span className="sm:hidden text-muted-foreground">Duration · </span>{formatDurationMillis(run.duration_millis)}</p>
        <p className="text-sm tabular-nums"><span className="sm:hidden text-muted-foreground">Reported tokens · </span>{formatTokenUsage(run.token_usage)}</p>
      </div>) : <div className="grid place-items-center p-12 text-sm text-muted-foreground">No completed runs with measured duration in this window.</div>}
    </Card>
    </section>
  </div>;
}

function Metric({ label, value }) { return <Card className="min-w-0 p-4 sm:p-5"><p className="text-xs font-medium text-muted-foreground sm:text-sm">{label}</p><p className="mt-2 text-xl font-semibold tracking-tight tabular-nums sm:text-3xl">{value}</p></Card>; }
function shortId(id) { const [, value = id] = id.split("_", 2); return value.slice(0, 8); }
