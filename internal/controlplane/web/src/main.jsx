import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { Activity, BarChart3, Bot, ChevronDown, GitBranch, Moon, Play, Plus, Server, Sun, Workflow, X } from "lucide-react";
import { Analytics } from "@/analytics";
import { AgentsPage, PipelinesPage, WorkersPage } from "@/catalog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { runDetails } from "@/run-metrics";
import { refreshAfterSubmit } from "@/status-refresh";
import "./styles.css";

const activeStates = new Set(["queued", "running"]);
const zeroTime = "0001-01-01T00:00:00Z";

function App() {
  const [status, setStatus] = useState({ jobs: [], workers: [], agents: [], pipelines: [], repositories: [], csrf_token: "" });
  const [selection, setSelection] = useState("");
  const [repository, setRepository] = useState("");
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState("");
  const [statusError, setStatusError] = useState("");
  const [statusLoaded, setStatusLoaded] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [composerOpen, setComposerOpen] = useState(false);
  const [filter, setFilter] = useState("all");
  const [expanded, setExpanded] = useState(new Set());
  const [dark, setDark] = useState(() => localStorage.getItem("machinist-theme") !== "light");
  const [view, setView] = useState(() => viewFromHash(window.location.hash));

  async function refresh() {
    const response = await fetch("/api/v1/status", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Status request failed (${response.status})`);
    const next = await response.json();
    setStatus(next);
    const available = new Set([
      ...next.pipelines.map((name) => `pipeline:${name}`),
      ...next.agents.map((name) => `agent:${name}`),
    ]);
    setSelection((current) => available.has(current) ? current : firstSelection(next));
    const availableRepositories = next.repositories || [];
    setRepository((current) => availableRepositories.includes(current) ? current : availableRepositories[0] || "");
    setStatusError("");
  }

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("machinist-theme", dark ? "dark" : "light");
  }, [dark]);

  useEffect(() => {
    const updateView = () => setView(viewFromHash(window.location.hash));
    window.addEventListener("hashchange", updateView);
    return () => window.removeEventListener("hashchange", updateView);
  }, []);

  useEffect(() => {
    let stopped = false;
    let timer;
    const load = async () => {
      try {
        await refresh();
        if (!stopped) setStatusLoaded(true);
      } catch (requestError) {
        if (!stopped) setStatusError(requestError.message);
      }
      if (!stopped) timer = window.setTimeout(load, 2000);
    };
    load();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, []);

  const choices = useMemo(() => [
    ...status.pipelines.map((name) => ({ value: `pipeline:${name}`, label: `Pipeline · ${name}` })),
    ...status.agents.map((name) => ({ value: `agent:${name}`, label: `Agent · ${name}` })),
  ], [status.agents, status.pipelines]);

  const repositories = status.repositories;

  const counts = useMemo(() => status.jobs.reduce((result, job) => {
    result.all += 1;
    if (activeStates.has(job.state)) result.active += 1;
    if (job.state === "failed" || job.state === "timed_out") result.failed += 1;
    if (job.state === "succeeded") result.succeeded += 1;
    return result;
  }, { all: 0, active: 0, failed: 0, succeeded: 0 }), [status.jobs]);

  const visibleJobs = useMemo(() => status.jobs.filter((job) => {
    if (filter === "active") return activeStates.has(job.state);
    if (filter === "failed") return job.state === "failed" || job.state === "timed_out";
    if (filter === "succeeded") return job.state === "succeeded";
    return true;
  }), [filter, status.jobs]);

  const latestWorkerSeen = status.workers.reduce((latest, worker) => !latest || Date.parse(worker.last_seen_at) > Date.parse(latest) ? worker.last_seen_at : latest, "");

  async function submit(event) {
    event.preventDefault();
    setSubmitting(true);
    setSubmitError("");
    const [kind, name] = selection.split(":", 2);
    try {
      const response = await fetch("/api/v1/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
        body: JSON.stringify({ prompt, repository, model: model.trim(), [kind]: name }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Submission failed (${response.status})`);
      }
      setPrompt("");
      setComposerOpen(false);
      await refreshAfterSubmit(refresh, setStatusError);
    } catch (requestError) {
      setSubmitError(requestError.message);
    } finally {
      setSubmitting(false);
    }
  }

  function toggleJob(id) {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }

  return (
    <div className="min-h-screen bg-background text-foreground md:flex">
      <aside className="sticky top-0 z-20 flex shrink-0 items-center border-b border-border bg-sidebar px-3 py-2 md:h-screen md:w-52 md:flex-col md:items-stretch md:border-b-0 md:border-r md:px-3 md:py-4">
        <div className="flex h-10 items-center gap-2.5 px-2 text-sm font-semibold tracking-tight">
          <span className="grid size-7 place-items-center rounded-md border border-border bg-surface text-primary shadow-xs"><Workflow className="size-4" /></span>
          <span>Machinist</span>
        </div>
        <nav className="ml-4 flex flex-1 gap-1 overflow-x-auto md:ml-0 md:mt-6 md:block md:overflow-visible" aria-label="Primary">
          <a href="#/runs" aria-current={view === "runs" ? "page" : undefined} className={cn("nav-item", view === "runs" && "nav-item-active")}><Activity className="size-4" /><span>Runs</span><span className="ml-auto text-xs text-muted-foreground">{counts.all}</span></a>
          <a href="#/analytics" aria-current={view === "analytics" ? "page" : undefined} className={cn("nav-item", view === "analytics" && "nav-item-active")}><BarChart3 className="size-4" /><span>Analytics</span></a>
          <a href="#/workers" aria-current={view === "workers" ? "page" : undefined} className={cn("nav-item", view === "workers" && "nav-item-active")}><Server className="size-4" /><span>Workers</span></a>
          <a href="#/agents" aria-current={view === "agents" ? "page" : undefined} className={cn("nav-item", view === "agents" && "nav-item-active")}><Bot className="size-4" /><span>Agents</span></a>
          <a href="#/pipelines" aria-current={view === "pipelines" ? "page" : undefined} className={cn("nav-item", view === "pipelines" && "nav-item-active")}><Workflow className="size-4" /><span>Pipelines</span></a>
        </nav>
        <div className="hidden border-t border-border pt-3 md:block">
          <div className="nav-item h-auto py-2"><Server className="size-4" /><span><span className="block">{status.workers.length} registered</span><span className="mt-0.5 block text-xs">{latestWorkerSeen ? `Last poll ${relativeTime(latestWorkerSeen)}` : "No workers"}</span></span></div>
          <button onClick={() => setDark((value) => !value)} className="nav-item w-full" aria-label={`Switch to ${dark ? "light" : "dark"} theme`}>
            {dark ? <Moon className="size-4" /> : <Sun className="size-4" />}<span>{dark ? "Dark" : "Light"} theme</span>
          </button>
        </div>
      </aside>

      <main className="min-w-0 flex-1">
        {view === "analytics" ? <Analytics jobs={status.jobs} loaded={statusLoaded} error={statusError} /> : view === "workers" ? <WorkersPage workers={status.workers} loaded={statusLoaded} error={statusError} /> : view === "agents" ? <AgentsPage /> : view === "pipelines" ? <PipelinesPage /> : <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
          <header className="flex items-start justify-between gap-6">
            <h1 className="text-xl font-semibold tracking-tight">Runs</h1>
            <div className="flex items-center gap-2">
              <Button variant="ghost" size="icon" className="md:hidden" onClick={() => setDark((value) => !value)} aria-label="Toggle theme">{dark ? <Moon className="size-4" /> : <Sun className="size-4" />}</Button>
              <Button className="text-xs!" onClick={() => setComposerOpen(true)}><Plus className="size-4" />New run</Button>
            </div>
          </header>

          {composerOpen && <RunComposer choices={choices} repositories={repositories} selection={selection} setSelection={setSelection} repository={repository} setRepository={setRepository} prompt={prompt} setPrompt={setPrompt} model={model} setModel={setModel} submitting={submitting} submit={submit} close={() => setComposerOpen(false)} />}
          {(statusError || submitError) && <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{submitError || statusError}</div>}

          <section>
            <div className="mb-3 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="flex items-center gap-1 overflow-x-auto" role="group" aria-label="Filter runs">
                {[["all", "All"], ["active", "Active"], ["failed", "Failed"], ["succeeded", "Succeeded"]].map(([value, label]) => (
                  <Button key={value} variant={filter === value ? "outline" : "ghost"} size="sm" aria-pressed={filter === value} onClick={() => setFilter(value)} className={cn("text-xs!", filter === value && "bg-surface")}>{label}<span className="text-muted-foreground">{counts[value]}</span></Button>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">{counts.all} run{counts.all === 1 ? "" : "s"}</p>
            </div>

            <Card className="overflow-hidden">
              <div className="hidden grid-cols-[7.5rem_minmax(10rem,1.2fr)_minmax(9rem,1fr)_minmax(9rem,1fr)_8rem_6.5rem] gap-4 border-b border-border bg-muted/35 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground xl:grid">
                <span>State</span><span>Run</span><span>Run with</span><span>Worker</span><span>Submitted</span><span />
              </div>
              {visibleJobs.length ? visibleJobs.map((job) => <RunRow key={job.id} job={job} open={expanded.has(job.id)} toggle={() => toggleJob(job.id)} />) : <EmptyRuns filtered={filter !== "all"} openComposer={() => setComposerOpen(true)} />}
            </Card>
          </section>

        </div>}
      </main>
    </div>
  );
}

function RunComposer({ choices, repositories, selection, setSelection, repository, setRepository, prompt, setPrompt, model, setModel, submitting, submit, close }) {
  return <Card className="overflow-hidden border-primary/25">
    <div className="flex items-center justify-between border-b border-border px-4 py-3 sm:px-5">
      <h2 className="text-sm font-semibold">New run</h2>
      <Button variant="ghost" size="icon" onClick={close} aria-label="Close new run form"><X className="size-4" /></Button>
    </div>
    <form onSubmit={submit} className="space-y-4 p-4 sm:p-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <label><span className="field-label">Run with</span><select className="field-control" value={selection} onChange={(event) => setSelection(event.target.value)} required>{choices.map((choice) => <option key={choice.value} value={choice.value}>{choice.label}</option>)}</select></label>
        <label><span className="field-label">Repository</span><select className="field-control font-mono" value={repository} onChange={(event) => setRepository(event.target.value)} disabled={!repositories.length} required>{repositories.length ? repositories.map((name) => <option key={name} value={name}>{name}</option>) : <option value="">No registered repositories</option>}</select></label>
      </div>
      <label><span className="field-label">Model <span className="font-normal normal-case tracking-normal text-muted-foreground">optional</span></span><input className="field-control font-mono" value={model} onChange={(event) => setModel(event.target.value)} placeholder="luna" maxLength={128} /></label>
      <label><span className="field-label">Prompt</span><textarea className="field-control min-h-28 resize-y" value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="Work on ticket https://github.com/acme/repo/issues/123" required /></label>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"><p className="text-xs text-muted-foreground">Machinist adds the selected agent template before dispatch.</p><Button disabled={submitting || !selection || !repository}>{submitting ? "Submitting…" : "Submit run"}<Play className="size-3.5" /></Button></div>
    </form>
  </Card>;
}

function RunRow({ job, open, toggle }) {
  const current = [...job.runs].reverse().find((run) => run.state !== "pending" && run.state !== "skipped") || job.runs[0];
  const detailsId = `${job.id}-steps`;
  return <article className="border-b border-border last:border-b-0">
    <button onClick={toggle} aria-expanded={open} aria-controls={detailsId} className="grid w-full gap-3 px-4 py-3.5 text-left transition hover:bg-muted/35 xl:grid-cols-[7.5rem_minmax(10rem,1.2fr)_minmax(9rem,1fr)_minmax(9rem,1fr)_8rem_6.5rem] xl:items-center xl:gap-4">
      <div className="flex items-center justify-between xl:block"><State value={job.state} /><span className="text-xs text-muted-foreground xl:hidden">{relativeTime(job.created_at)}</span></div>
      <div className="min-w-0"><p className="font-mono text-sm font-medium">{shortId(job.id)}</p><p className="mt-1 break-all text-xs text-muted-foreground xl:truncate">{job.repository}</p></div>
      <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground"><SelectionIcon kind={job.selection_kind} /><span className="min-w-0 flex-1 truncate text-foreground">{job.selection_name}</span><span className="shrink-0 capitalize">{job.selection_kind}</span></div>
      <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground"><Server className="size-3.5 shrink-0" /><span className="truncate">{current?.worker_name || "Unassigned"}</span></div>
      <time className="hidden text-xs text-muted-foreground xl:block" dateTime={job.created_at}>{relativeTime(job.created_at)}</time>
      <div className="flex items-center justify-between text-xs text-muted-foreground xl:justify-end"><span>{job.runs.length} step{job.runs.length === 1 ? "" : "s"}</span><ChevronDown className={cn("ml-2 size-4 transition-transform", open && "rotate-180")} /></div>
    </button>
    {open && <RunSteps id={detailsId} job={job} />}
  </article>;
}

function RunSteps({ id, job }) {
  return <div id={id} className="border-t border-border bg-muted/20 px-4 py-4 xl:pl-[9rem]"><div className="grid gap-2 xl:grid-cols-3">{job.runs.map((run, index) => <div key={run.id} className="flex min-w-0 items-start gap-3 rounded-md border border-border bg-surface p-3">
    <span className="grid size-6 shrink-0 place-items-center rounded-full border border-border font-mono text-xs text-muted-foreground">{index + 1}</span>
    <div className="min-w-0 flex-1"><div className="flex items-center justify-between gap-2"><p className="truncate text-sm font-medium capitalize">{run.agent}</p><State value={run.state} /></div><p className="mt-1 truncate text-xs text-muted-foreground">{runDetails(run)}</p>{run.error && <p className="mt-2 break-words text-xs text-danger">{run.error}</p>}</div>
  </div>)}</div></div>;
}

function State({ value }) {
  const tones = { running: "border-warning/25 bg-warning/10 text-warning", queued: "border-warning/25 bg-warning/10 text-warning", succeeded: "border-success/25 bg-success/10 text-success", failed: "border-danger/25 bg-danger/10 text-danger", timed_out: "border-danger/25 bg-danger/10 text-danger", cancelled: "border-danger/25 bg-danger/10 text-danger", pending: "border-border bg-muted text-muted-foreground", skipped: "border-border bg-muted text-muted-foreground" };
  return <Badge className={cn("gap-1.5", tones[value] || tones.pending)}><span className="size-1.5 rounded-full bg-current" />{value.replaceAll("_", " ")}</Badge>;
}

function SelectionIcon({ kind }) { return kind === "pipeline" ? <Workflow className="size-3.5 shrink-0" /> : <Bot className="size-3.5 shrink-0" />; }

function EmptyRuns({ filtered, openComposer }) {
  return <div className="grid place-items-center px-6 py-16 text-center"><span className="grid size-10 place-items-center rounded-full bg-muted text-muted-foreground"><GitBranch className="size-5" /></span><h3 className="mt-3 text-sm font-semibold">{filtered ? "No matching runs" : "No runs yet"}</h3><p className="mt-1 max-w-sm text-xs leading-5 text-muted-foreground">{filtered ? "Try a different state filter." : "Submit a prompt to an agent or pipeline. It will appear here as soon as the control plane admits it."}</p>{!filtered && <Button variant="outline" size="sm" className="mt-4" onClick={openComposer}><Plus className="size-3.5" />New run</Button>}</div>;
}

function firstSelection(status) { if (status.pipelines?.length) return `pipeline:${status.pipelines[0]}`; if (status.agents?.length) return `agent:${status.agents[0]}`; return ""; }
function viewFromHash(hash) { const value = hash.replace(/^#\//, ""); return ["runs", "analytics", "workers", "agents", "pipelines"].includes(value) ? value : "runs"; }
function shortId(id) { const [, value = id] = id.split("_", 2); return value.slice(0, 8); }
function relativeTime(value) { if (!value || value === zeroTime) return "Not started"; const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1000)); if (seconds < 10) return "just now"; if (seconds < 60) return `${seconds}s ago`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes}m ago`; const hours = Math.floor(minutes / 60); if (hours < 24) return `${hours}h ago`; return `${Math.floor(hours / 24)}d ago`; }
createRoot(document.getElementById("root")).render(<App />);
