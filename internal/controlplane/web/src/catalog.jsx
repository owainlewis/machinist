import { Tabs } from "@/components/ui/tabs";
import { useEffect, useState } from "react";
import { Server } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { PageHeading, QuietState } from "@/components/ui/page-heading";

export function WorkersPage({ workers, loaded, error }) {
  return <Page title="Workers" description="The machines available to pick up and execute work.">{error && <Failure value={error} />}{!loaded && !error ? <Loading description="Checking live worker status." /> : loaded && (workers.length ? <Card className="overflow-hidden">{workers.map((worker) => <article key={worker.instance_id} className="grid gap-4 border-b border-border p-4 last:border-b-0 sm:grid-cols-[minmax(12rem,1fr)_minmax(12rem,1fr)_10rem] sm:items-center sm:px-5">
    <div className="min-w-0"><div className="flex items-center gap-2"><Server className="size-4 text-muted-foreground" /><h2 className="truncate text-sm font-medium">{worker.name}</h2></div><p className="mt-1 truncate font-mono text-xs text-muted-foreground">{worker.instance_id}</p></div>
    <div className="flex flex-wrap gap-1.5">{worker.repositories?.length ? worker.repositories.map((repository) => <Badge key={repository} className="border-border bg-muted font-mono text-muted-foreground">{repository}</Badge>) : <span className="text-xs text-muted-foreground">No repositories</span>}</div>
    <div className="flex items-center justify-between gap-2 sm:flex-col sm:items-end"><Badge className={worker.connected ? "gap-1.5 border-success/25 bg-success/10 text-success" : "gap-1.5 border-border bg-muted text-muted-foreground"}><span className="size-1.5 rounded-full bg-current" />{worker.connected ? "Connected" : "Disconnected"}</Badge><time className="text-xs text-muted-foreground sm:text-right" dateTime={worker.last_seen_at} title={new Date(worker.last_seen_at).toLocaleString()}>Last seen {relativeTime(worker.last_seen_at)}</time></div>
  </article>)}</Card> : <Empty value="No workers registered." description="Start a worker to register this machine with the control plane." />)}</Page>;
}

const displayName = name => String(name).replaceAll("_", " ").replaceAll("-", " ").replace(/^./, c => c.toUpperCase());

export function CommandsPage() {
  const definitions = useDefinitions();
  const [selection, setSelection] = useState("");
  const data = definitions.value;
  const names = Object.keys(data.workflows || {});
  const selected = names.includes(selection) ? selection : names[0];
  const steps = data.workflows?.[selected] || [];
  const commands = steps.map(step => data.commands.find(command => command.name === step.name));
  return <Page title="Workflows" description="Choose how a task gets done, from one agent to a sequence of steps.">
    {definitions.loading ? <Loading /> : definitions.error ? <Failure value={definitions.error} /> : names.length ? <div className="max-w-4xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <label className="block w-full max-w-sm"><span className="field-label">Workflow</span><select className="field-control" value={selected} onChange={event=>setSelection(event.target.value)}>{names.map(name=><option key={name} value={name}>{displayName(name)}</option>)}</select></label>
        <p className="text-sm text-muted-foreground">{steps.length} step{steps.length === 1 ? "" : "s"} · Runs in order</p>
      </div>
      <Tabs key={selected} label="Workflow information" items={[
        {id:"steps",label:"Steps",content:<section className="space-y-4">
          <ol className="overflow-hidden rounded-lg border border-border bg-surface">{steps.map((step,index)=><li key={index} className="flex items-start gap-4 border-b border-border p-5 last:border-0">
            <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-muted text-xs text-muted-foreground">{index+1}</span>
            <div className="min-w-0 flex-1"><h2 className="text-sm font-semibold">{displayName(step.name)}</h2><p className="mt-1 text-sm text-muted-foreground">{step.approval ? "Waits for your approval before starting." : index === 0 ? "Starts when you submit a task." : "Starts when the previous step completes."}</p></div>
            {step.approval && <Badge className="shrink-0 border-warning/25 bg-warning/10 text-warning">Approval</Badge>}
          </li>)}</ol>
          <p className="text-sm leading-6 text-muted-foreground">Files carry forward automatically between steps. Read and write them at <code>{"{{task.output_dir}}"}</code>. A failed or blocked step pauses the task. A policy step can also request approval.</p>
          <p className="text-xs text-muted-foreground">Choose this workflow when creating a new task. Edit workflow definitions in config.toml.</p>
        </section>},
        {id:"prompts",label:"Prompts",content:<div className="space-y-5">{steps.map((step,index)=><Card key={index} className="overflow-hidden"><header className="flex flex-wrap justify-between gap-2 border-b border-border px-5 py-3"><h2 className="text-sm font-semibold">{index+1}. {displayName(step.name)}</h2><p className="text-xs text-muted-foreground">{commands[index]?.executor} · {commands[index]?.timeout}</p></header><pre tabIndex={0} aria-label={`${step.name} prompt template`} className="max-h-96 overflow-auto whitespace-pre-wrap break-words p-5 font-mono text-xs leading-6">{commands[index]?.prompt || "Uses the task instructions directly."}</pre></Card>)}</div>},
        {id:"help",label:"Template help",content:<TemplateHelp />},
      ]} />
    </div> : data.commands.length ? <div className="max-w-4xl space-y-5"><p className="text-sm text-muted-foreground">These commands can run directly. To give a task multiple steps, add a workflow in config.toml.</p><pre className="rounded-lg border border-border p-4 text-sm">{`[workflows.deliver]\nsteps = ["${data.commands[0].name}"]`}</pre>{data.commands.map(command=><Card key={command.name} className="p-5"><h2 className="text-sm font-semibold">{displayName(command.name)}</h2><p className="mt-1 text-xs text-muted-foreground">{command.executor} · {command.timeout}</p></Card>)}</div> : <Empty value="No workflows configured." />}
  </Page>;
}

function TemplateHelp() {
  return <section className="space-y-5 text-sm"><h2 className="font-semibold">Write a reusable prompt</h2><p className="text-muted-foreground">The task supplies the request. Your prompt tells an agent or script what to do with it.</p><dl className="grid gap-4 sm:grid-cols-2">{[
    ["{{task.spec}}", "The instructions entered when creating the task."],
    ["{{task.title}}", "The task’s title."],
    ["{{task.source_url}}", "The linked issue or other source, if supplied."],
    ["{{task.output_dir}}", "Shared task files. Write plan.md here and read it in the next step."],
  ].map(([field,description])=><div key={field}><dt className="font-mono text-xs">{field}</dt><dd className="mt-1 text-muted-foreground">{description}</dd></div>)}</dl>
  <pre className="overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-surface p-5 font-mono text-xs leading-6">{"Task: {{task.title}}\nSource: {{task.source_url}}\nRequirements: {{task.spec}}\n\nRead the source if no requirements are supplied.\nSave the finished report to {{task.output_dir}}/report.md."}</pre>
  <p className="text-muted-foreground">Only save deliverables in the shared folder. Use <code>MACHINIST_SCRATCH_DIR</code> for temporary scripts, clones and logs.</p></section>;
}

function Page({ title, description, children }) { return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8"><PageHeading title={title} description={description} />{children}</div>; }
function Loading({ description = "Loading the latest configuration." }) { return <Card><QuietState title="Preparing the bench" description={description} role="status" /></Card>; }
function Empty({ value, description = "Configuration added on the control plane will appear here." }) { return <Card><QuietState title={value} description={description} /></Card>; }
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
