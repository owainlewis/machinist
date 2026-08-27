import { Clock3, GitBranch, TimerReset } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { triggerHealthTone, triggerView } from "@/trigger-state";

export function TriggersPage({ triggers = [], loaded, error }) {
  return <Page title="Triggers">
    {error && <Failure value={error} />}
    {!loaded && !error ? <Loading /> : loaded && (triggers.length ? <div className="grid gap-4 xl:grid-cols-2">{triggers.map((trigger) => <TriggerCard key={trigger.identity} trigger={trigger} />)}</div> : <Empty />)}
  </Page>;
}

function TriggerCard({ trigger }) {
  const view = triggerView(trigger);
  const tone = triggerHealthTone(view.health);
  const healthClass = {
    success: "border-success/25 bg-success/10 text-success",
    warning: "border-warning/25 bg-warning/10 text-warning",
    danger: "border-danger/25 bg-danger/10 text-danger",
    neutral: "border-border bg-muted text-muted-foreground",
  }[tone];
  return <Card className={cn("overflow-hidden", tone === "danger" && "border-danger/40")}>
    <header className="flex flex-col gap-3 border-b border-border p-4 sm:flex-row sm:items-center sm:justify-between sm:px-5">
      <div className="min-w-0"><div className="flex items-center gap-2"><TimerReset className="size-4 shrink-0 text-muted-foreground" /><h2 className="truncate font-mono text-sm font-semibold" title={view.identity}>{view.identity}</h2></div><p className="mt-1 capitalize text-xs text-muted-foreground">{view.family} trigger</p></div>
      <Badge className={cn("shrink-0 gap-1.5 capitalize", healthClass)}><span className="size-1.5 rounded-full bg-current" />{view.health}</Badge>
    </header>
    <dl className="grid gap-x-6 gap-y-3 p-4 text-xs sm:grid-cols-2 sm:p-5">{view.rows.map((row) => <div key={row.field} className="min-w-0"><dt className="text-muted-foreground">{row.label}</dt><dd className={cn("mt-1 break-all font-medium", row.field.endsWith("_count") && "tabular-nums", row.field.includes("due") || row.field.includes("attempt") || row.field.includes("success") ? "font-mono" : "")}><TriggerValue field={row.field} value={row.value} /></dd></div>)}</dl>
    {view.error && <div role="alert" className="flex items-start gap-2 border-t border-danger/25 bg-danger/5 px-4 py-3 text-xs text-danger sm:px-5"><GitBranch className="mt-0.5 size-3.5 shrink-0" /><p className="break-words">{view.error}</p></div>}
  </Card>;
}

function TriggerValue({ field, value }) {
  if ((field === "next_due" || field === "last_attempt" || field === "last_success") && value !== "Not yet") return <time dateTime={value} title={formatDate(value)}>{formatDate(value)}</time>;
  if (field === "active_job" && value !== "None") return <span className="inline-flex items-center gap-1.5"><Clock3 className="size-3.5 text-muted-foreground" />{value}</span>;
  return value;
}

function formatDate(value) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}

function Page({ title, children }) { return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8"><h1 className="text-xl font-semibold tracking-tight">{title}</h1>{children}</div>; }
function Loading() { return <Card className="p-8 text-center text-sm text-muted-foreground">Loading…</Card>; }
function Failure({ value }) { return <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{value}</div>; }
function Empty() { return <Card className="p-8 text-center text-sm text-muted-foreground">No managed triggers configured.</Card>; }
