import { useEffect, useState } from "react";
import { Bot, Clock3, Hash, Server, Terminal } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";

export function WorkersPage({ workers, loaded, error }) {
  return <Page title="Workers">{error && <Failure value={error} />}{!loaded && !error ? <Loading /> : loaded && (workers.length ? <Card className="overflow-hidden">{workers.map((worker) => <article key={worker.instance_id} className="grid gap-4 border-b border-border p-4 last:border-b-0 sm:grid-cols-[minmax(12rem,1fr)_minmax(12rem,1fr)_10rem] sm:items-center sm:px-5">
    <div className="min-w-0"><div className="flex items-center gap-2"><Server className="size-4 text-muted-foreground" /><h2 className="truncate text-sm font-medium">{worker.name}</h2></div><p className="mt-1 truncate font-mono text-xs text-muted-foreground">{worker.instance_id}</p></div>
    <div className="flex flex-wrap gap-1.5">{worker.repositories?.length ? worker.repositories.map((repository) => <Badge key={repository} className="border-border bg-muted font-mono text-muted-foreground">{repository}</Badge>) : <span className="text-xs text-muted-foreground">No repositories</span>}</div>
    <div className="flex items-center justify-between gap-2 sm:flex-col sm:items-end"><Badge className={worker.connected ? "gap-1.5 border-success/25 bg-success/10 text-success" : "gap-1.5 border-border bg-muted text-muted-foreground"}><span className="size-1.5 rounded-full bg-current" />{worker.connected ? "Connected" : "Disconnected"}</Badge><time className="text-xs text-muted-foreground sm:text-right" dateTime={worker.last_seen_at} title={new Date(worker.last_seen_at).toLocaleString()}>Last seen {relativeTime(worker.last_seen_at)}</time></div>
  </article>)}</Card> : <Empty value="No workers registered." />)}</Page>;
}

export function CommandsPage() {
  const definitions = useDefinitions();
  return <Page title="Commands">{definitions.loading ? <Loading /> : definitions.error ? <Failure value={definitions.error} /> : definitions.value.commands.length ? <div className="space-y-3">{definitions.value.commands.map((command) => <Card key={command.name} className="overflow-hidden">
    <div className="flex flex-col gap-3 border-b border-border p-4 sm:flex-row sm:items-center sm:justify-between sm:px-5"><div className="flex items-center gap-2"><Bot className="size-4 text-muted-foreground" /><h2 className="text-sm font-semibold capitalize">{command.name}</h2></div><div className="flex flex-wrap gap-1.5"><Badge className="border-border bg-muted text-muted-foreground"><Terminal className="mr-1 size-3" />{command.executor}</Badge><Badge className="border-border bg-muted text-muted-foreground"><Clock3 className="mr-1 size-3" />{command.timeout}</Badge></div></div>
    <details><summary className="cursor-pointer px-4 py-3 text-sm font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50 sm:px-5">Prompt template</summary><pre tabIndex={0} role="region" aria-label={`${command.name} prompt template`} className="max-h-[32rem] overflow-auto border-t border-border bg-background p-4 whitespace-pre-wrap font-mono text-xs leading-5 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50 sm:p-5">{command.prompt}</pre></details>
    <div className="flex items-center gap-2 border-t border-border px-4 py-2.5 font-mono text-xs text-muted-foreground sm:px-5"><Hash className="size-3.5 shrink-0" /><span className="truncate" title={command.hash}>{command.hash}</span></div>
  </Card>)}</div> : <Empty value="No commands configured." />}</Page>;
}

function Page({ title, children }) { return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8"><h1 className="text-xl font-semibold tracking-tight">{title}</h1>{children}</div>; }
function Loading() { return <Card className="p-8 text-center text-sm text-muted-foreground">Loading…</Card>; }
function Empty({ value }) { return <Card className="p-8 text-center text-sm text-muted-foreground">{value}</Card>; }
function Failure({ value }) { return <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{value}</div>; }

function useDefinitions() {
  const [result, setResult] = useState({ loading: true, error: "", value: { commands: [] } });
  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/v1/definitions", { headers: { Accept: "application/json" }, signal: controller.signal }).then(async (response) => {
      if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || `Definitions request failed (${response.status})`); }
      return response.json();
    }).then((value) => setResult({ loading: false, error: "", value })).catch((error) => { if (error.name !== "AbortError") setResult((current) => ({ ...current, loading: false, error: error.message })); });
    return () => controller.abort();
  }, []);
  return result;
}

function relativeTime(value) { const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1000)); if (seconds < 10) return "just now"; if (seconds < 60) return `${seconds}s ago`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes}m ago`; const hours = Math.floor(minutes / 60); if (hours < 24) return `${hours}h ago`; return `${Math.floor(hours / 24)}d ago`; }
