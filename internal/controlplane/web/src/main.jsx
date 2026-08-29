import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Activity, ArrowLeft, BarChart3, Bot, GitBranch, LayoutDashboard, Moon, Play, Plus, Server, Sun, Table2, TimerReset, Trash2, Workflow, X } from "lucide-react";
import { Analytics } from "@/analytics";
import { CommandsPage, WorkersPage } from "@/catalog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { formatDurationMillis, formatTaskTokenUsage, formatTokenUsage, runModelSummary, taskDurationMillis, tokenUsageSummary } from "@/run-metrics";
import { routeFromHash } from "@/routes";
import { boardColumns, currentRun, filterJobs, githubIssueReference, groupJobsByBoardColumn, jobCounts, jobDisplayTitle, needsAttention } from "@/runs-board";
import { createStatusLoader } from "@/status-loader";
import { TriggersPage } from "@/triggers";
import "./styles.css";

const zeroTime = "0001-01-01T00:00:00Z";

function App() {
  const [status, setStatus] = useState({ jobs: [], workers: [], commands: [], repositories: [], triggers: [], csrf_token: "" });
  const [selection, setSelection] = useState("");
  const [repository, setRepository] = useState("");
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState("");
  const [statusError, setStatusError] = useState("");
  const [statusLoaded, setStatusLoaded] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const [taskActionError, setTaskActionError] = useState("");
  const [deletingJob, setDeletingJob] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [composerOpen, setComposerOpen] = useState(false);
  const [filter, setFilter] = useState("all");
  const [runsView, setRunsView] = useState("board");
  const [dark, setDark] = useState(() => localStorage.getItem("machinist-theme") !== "light");
  const [route, setRoute] = useState(() => routeFromHash(window.location.hash));
  const view = route.view;
  const statusLoader = useRef(null);
  if (!statusLoader.current) statusLoader.current = createStatusLoader({
    request: async () => {
      const response = await fetch("/api/v1/status", { headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Status request failed (${response.status})`);
      return response.json();
    },
    apply: (result) => {
      if (result.kind === "error") {
        setStatusError(result.message);
        return;
      }
      const next = result.status;
      setStatus(next);
      setStatusError("");
      setStatusLoaded(true);
      const available = new Set(next.commands);
      setSelection((current) => available.has(current) ? current : firstSelection(next));
      const availableRepositories = next.repositories || [];
      setRepository((current) => availableRepositories.includes(current) ? current : availableRepositories[0] || "");
    },
  });

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("machinist-theme", dark ? "dark" : "light");
  }, [dark]);

  useEffect(() => {
    const updateView = () => {
      setTaskActionError("");
      setRoute(routeFromHash(window.location.hash));
    };
    window.addEventListener("hashchange", updateView);
    return () => window.removeEventListener("hashchange", updateView);
  }, []);

  useEffect(() => {
    let stopped = false;
    let timer;
    const load = async () => {
      await statusLoader.current.refresh();
      if (!stopped) timer = window.setTimeout(load, 2000);
    };
    load();
    return () => {
      stopped = true;
      statusLoader.current.cancel();
      window.clearTimeout(timer);
    };
  }, []);

  const choices = useMemo(() => status.commands.map((name) => ({ value: name, label: name })), [status.commands]);

  const repositories = status.repositories;

  const counts = useMemo(() => jobCounts(status.jobs), [status.jobs]);
  const visibleJobs = useMemo(() => filterJobs(status.jobs, filter), [filter, status.jobs]);

  const connectedWorkers = status.workers.filter((worker) => worker.connected).length;
  const selectedJob = route.jobID ? status.jobs.find((job) => job.id === route.jobID) : undefined;

  async function submit(event) {
    event.preventDefault();
    setSubmitting(true);
    setSubmitError("");
    try {
      const response = await fetch("/api/v1/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
        body: JSON.stringify({ prompt, repository, model: model.trim(), command: selection }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Submission failed (${response.status})`);
      }
      setPrompt("");
      setComposerOpen(false);
      await statusLoader.current.refresh();
    } catch (requestError) {
      setSubmitError(requestError.message);
    } finally {
      setSubmitting(false);
    }
  }

  async function deleteJob(job) {
    if (!window.confirm(`Delete task ${shortId(job.id)} and all of its stored run data?`)) return;
    setDeletingJob(job.id);
    setTaskActionError("");
    try {
      const response = await fetch(`/api/v1/jobs/${encodeURIComponent(job.id)}`, {
        method: "DELETE",
        headers: { "X-Machinist-CSRF": status.csrf_token },
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Delete failed (${response.status})`);
      }
      await statusLoader.current.refresh();
      window.location.hash = "#/runs";
    } catch (requestError) {
      setTaskActionError(requestError.message);
    } finally {
      setDeletingJob("");
    }
  }

  return (
    <div className="min-h-screen bg-background text-foreground md:flex">
      <aside className="sticky top-0 z-20 flex shrink-0 items-center border-b border-border bg-sidebar px-3 py-2 md:h-screen md:w-52 md:flex-col md:items-stretch md:border-b-0 md:border-r md:px-3 md:py-4">
        <div className="flex h-10 items-center gap-2.5 px-2 text-sm font-semibold tracking-tight">
          <span className="grid size-7 place-items-center rounded-md border border-border bg-surface text-primary shadow-xs"><Workflow className="size-[18px]" /></span>
          <span>Machinist</span>
        </div>
        <nav className="ml-4 flex flex-1 gap-1 overflow-x-auto md:ml-0 md:mt-6 md:block md:overflow-visible" aria-label="Primary">
          <a href="#/runs" aria-current={view === "runs" || view === "task" ? "page" : undefined} className={cn("nav-item", (view === "runs" || view === "task") && "nav-item-active")}><Activity className="size-4" /><span>Runs</span><span className="ml-auto text-xs text-muted-foreground">{counts.all}</span></a>
          <a href="#/analytics" aria-current={view === "analytics" ? "page" : undefined} className={cn("nav-item", view === "analytics" && "nav-item-active")}><BarChart3 className="size-4" /><span>Analytics</span></a>
          <a href="#/workers" aria-current={view === "workers" ? "page" : undefined} className={cn("nav-item", view === "workers" && "nav-item-active")}><Server className="size-4" /><span>Workers</span></a>
          <a href="#/triggers" aria-current={view === "triggers" ? "page" : undefined} className={cn("nav-item", view === "triggers" && "nav-item-active")}><TimerReset className="size-4" /><span>Triggers</span><span className="ml-auto text-xs text-muted-foreground">{status.triggers?.length || 0}</span></a>
          <a href="#/commands" aria-current={view === "commands" ? "page" : undefined} className={cn("nav-item", view === "commands" && "nav-item-active")}><Bot className="size-4" /><span>Commands</span></a>
        </nav>
        <div className="hidden border-t border-border pt-3 md:block">
          <div className="nav-item" title={`${connectedWorkers} connected · ${status.workers.length} registered`}><Server className="size-4 shrink-0" /><span className="min-w-0 truncate whitespace-nowrap">{connectedWorkers ? `${connectedWorkers} worker${connectedWorkers === 1 ? "" : "s"} online` : "No workers online"}</span></div>
          <button onClick={() => setDark((value) => !value)} className="nav-item w-full" aria-label={`Switch to ${dark ? "light" : "dark"} theme`}>
            {dark ? <Moon className="size-4" /> : <Sun className="size-4" />}<span>{dark ? "Dark" : "Light"} theme</span>
          </button>
        </div>
      </aside>

      <main className="min-w-0 flex-1">
        {view === "task" ? <TaskDetail job={selectedJob} loaded={statusLoaded} error={statusError || taskActionError} deleting={deletingJob === route.jobID} onDelete={deleteJob} /> : view === "analytics" ? <Analytics jobs={status.jobs} loaded={statusLoaded} error={statusError} /> : view === "workers" ? <WorkersPage workers={status.workers} loaded={statusLoaded} error={statusError} /> : view === "triggers" ? <TriggersPage triggers={status.triggers || []} loaded={statusLoaded} error={statusError} /> : view === "commands" ? <CommandsPage /> : <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
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
            <div className="mb-3 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Filter runs">
                {[["all", "All"], ["active", "Active"], ["failed", "Failed"], ["succeeded", "Succeeded"]].map(([value, label]) => (
                  <Button key={value} variant={filter === value ? "outline" : "ghost"} size="sm" aria-pressed={filter === value} onClick={() => setFilter(value)} className={cn("text-xs!", filter === value && "bg-surface")}>{label}<span className="text-muted-foreground">{counts[value]}</span></Button>
                ))}
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3 lg:justify-end">
                <p className="text-xs text-muted-foreground">{counts.all} run{counts.all === 1 ? "" : "s"}</p>
                <div className="inline-flex rounded-md border border-border bg-surface p-0.5" role="group" aria-label="Runs view">
                  <Button variant={runsView === "board" ? "outline" : "ghost"} size="sm" className={cn("h-7 border-transparent px-2.5 text-xs!", runsView === "board" && "border-border bg-muted")} aria-pressed={runsView === "board"} onClick={() => setRunsView("board")}><LayoutDashboard className="size-3.5" />Board</Button>
                  <Button variant={runsView === "table" ? "outline" : "ghost"} size="sm" className={cn("h-7 border-transparent px-2.5 text-xs!", runsView === "table" && "border-border bg-muted")} aria-pressed={runsView === "table"} onClick={() => setRunsView("table")}><Table2 className="size-3.5" />Table</Button>
                </div>
              </div>
            </div>

            {runsView === "board" ? <RunBoard jobs={visibleJobs} /> : <Card className="overflow-hidden">
              <div className="hidden grid-cols-[6.5rem_minmax(9rem,1.1fr)_minmax(8rem,0.9fr)_minmax(8rem,1fr)_minmax(8rem,1fr)_7rem_9rem] gap-4 border-b border-border bg-muted/35 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground xl:grid">
                <span>State</span><span>Run</span><span>Run with</span><span>Worker</span><span>Model</span><span>Submitted</span><span>Usage</span>
              </div>
              {visibleJobs.length ? visibleJobs.map((job) => <RunRow key={job.id} job={job} />) : <EmptyRuns filtered={filter !== "all"} openComposer={() => setComposerOpen(true)} />}
            </Card>}
          </section>

        </div>}
      </main>
    </div>
  );
}

function TaskDetail({ job, loaded, error, deleting, onDelete }) {
  if (!loaded && !error) return <div className="mx-auto max-w-[1100px] p-4 sm:p-6 lg:p-8"><p className="text-sm text-muted-foreground">Loading task…</p></div>;
  if (!job) return <div className="mx-auto max-w-[1100px] space-y-6 p-4 sm:p-6 lg:p-8"><Button asChild variant="ghost" size="sm"><a href="#/runs"><ArrowLeft className="size-4" />Back to runs</a></Button>{error && <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>}<div className="border-y border-border py-12 text-center"><h1 className="text-xl font-semibold">Task not found</h1><p className="mt-1 text-sm text-muted-foreground">It may have been deleted.</p></div></div>;
  const usage = tokenUsageSummary(job.runs);
  const totalDuration = taskDurationMillis(job.runs);
  const terminal = job.state === "succeeded" || job.state === "failed";
  return <div className="mx-auto max-w-[1100px] space-y-8 p-4 sm:p-6 lg:p-8">
    <header className="space-y-4">
      <Button asChild variant="ghost" size="sm" className="-ml-3"><a href="#/runs"><ArrowLeft className="size-4" />Back to runs</a></Button>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0"><div className="flex flex-wrap items-center gap-2"><h1 className="truncate text-xl font-semibold" title={jobDisplayTitle(job)}>{jobDisplayTitle(job)}</h1><State value={job.state} /></div><p className="mt-1 break-all font-mono text-xs text-muted-foreground">{githubIssueReference(job) || shortId(job.id)}{githubIssueReference(job) ? ` · ${shortId(job.id)}` : ""}</p><p className="mt-1 break-all font-mono text-xs text-muted-foreground">{job.id}</p></div>
        <Button variant="outline" className="self-start border-danger/35 text-danger hover:bg-danger/10" disabled={!terminal || deleting} onClick={() => onDelete(job)} title={terminal ? "Delete this task and its stored run data" : "Active tasks cannot be deleted"}><Trash2 className="size-4" />{deleting ? "Deleting…" : "Delete task"}</Button>
      </div>
      {error && <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>}
    </header>

    <dl className="grid border-y border-border sm:grid-cols-2 lg:grid-cols-4">
      <DetailMetric label="Repository" value={job.repository} mono />
      <DetailMetric label="Command" value={job.command} />
      <DetailMetric label="Requested model" value={runModelSummary(job.runs)} mono />
      <DetailMetric label="Submitted" value={formatTimestamp(job.created_at)} />
      <DetailMetric label="Runs" value={`${job.runs.length}`} />
      <DetailMetric label="Duration" value={totalDuration === undefined ? "Unavailable" : formatDurationMillis(totalDuration)} />
      <DetailMetric label="Token usage" value={formatTaskTokenUsage(usage)} />
      <DetailMetric label="Updated" value={formatTimestamp(job.updated_at)} />
    </dl>

    <section aria-labelledby="task-prompt">
      <h2 id="task-prompt" className="text-sm font-semibold">Prompt</h2>
      <pre className="mt-3 whitespace-pre-wrap break-words border-l-2 border-border pl-4 font-sans text-sm leading-6">{job.prompt}</pre>
    </section>

    <section aria-labelledby="task-execution">
      <div className="flex items-center justify-between gap-4"><h2 id="task-execution" className="text-sm font-semibold">Execution</h2><span className="text-xs text-muted-foreground">{job.runs.length} run{job.runs.length === 1 ? "" : "s"}</span></div>
      <ol className="mt-3 grid gap-3">{job.runs.map((run, index) => <li key={run.id}><Card className="min-w-0 p-4">
        <div className="flex min-w-0 items-start gap-3"><span className="grid size-7 shrink-0 place-items-center rounded-full border border-border font-mono text-xs text-muted-foreground">{index + 1}</span><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center justify-between gap-2"><div className="min-w-0"><h3 className="truncate text-sm font-semibold">{run.command}</h3><p className="mt-0.5 truncate font-mono text-xs text-muted-foreground" title={run.id}>{run.id}</p></div><State value={run.state} /></div>
          <dl className="mt-4 grid gap-x-6 gap-y-3 border-t border-border pt-3 sm:grid-cols-2 lg:grid-cols-4"><RunMetric label="Executor" value={run.executor} mono /><RunMetric label="Requested model" value={run.model || "Executor default"} mono /><RunMetric label="Worker" value={run.worker_name || "Unassigned"} /><RunMetric label="Duration" value={Number.isSafeInteger(run.duration_millis) ? formatDurationMillis(run.duration_millis) : "Unavailable"} /><RunMetric label="Tokens" value={formatTokenUsage(run.token_usage) === "Unavailable" ? "Not reported" : `${formatTokenUsage(run.token_usage)} tokens`} /><RunMetric label="Started" value={formatTimestamp(run.started_at)} /><RunMetric label="Completed" value={formatTimestamp(run.completed_at)} /><RunMetric label="Exit code" value={run.exit_code === undefined ? "Unavailable" : String(run.exit_code)} /></dl>
          {run.error && <div className="mt-4 border-l-2 border-danger pl-3"><p className="text-xs font-medium text-danger">Error</p><p className="mt-1 break-words text-sm text-danger">{run.error}</p></div>}
        </div></div>
      </Card></li>)}</ol>
    </section>
  </div>;
}

function DetailMetric({ label, value, mono = false }) { return <div className="min-w-0 border-b border-border py-3 last:border-b-0 sm:border-r sm:px-4 sm:first:pl-0 lg:border-b-0"><dt className="text-xs text-muted-foreground">{label}</dt><dd className={cn("mt-1 truncate text-sm font-medium", mono && "font-mono")} title={value}>{value}</dd></div>; }
function RunMetric({ label, value, mono = false }) { return <div className="min-w-0"><dt className="text-xs text-muted-foreground">{label}</dt><dd className={cn("mt-0.5 truncate text-sm", mono && "font-mono")} title={value}>{value}</dd></div>; }

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
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"><p className="text-xs text-muted-foreground">The prompt is sent to the command on standard input.</p><Button disabled={submitting || !selection || !repository}>{submitting ? "Submitting…" : "Submit run"}<Play className="size-3.5" /></Button></div>
    </form>
  </Card>;
}

function RunBoard({ jobs }) {
  const groupedJobs = groupJobsByBoardColumn(jobs);
  return <div className="grid min-w-0 gap-4 lg:grid-cols-3">
    {boardColumns.map((column) => <section key={column.id} className="min-w-0 rounded-lg border border-border bg-muted/20" aria-labelledby={`board-${column.id}`}>
      <header className="flex items-center justify-between gap-3 border-b border-border px-3 py-2.5">
        <div className="min-w-0"><h2 id={`board-${column.id}`} className="text-sm font-semibold">{column.title}</h2><p className="break-words text-xs text-muted-foreground">{column.description}</p></div>
        <Badge className="shrink-0 border-border bg-surface text-muted-foreground" aria-label={`${groupedJobs[column.id].length} visible ${column.title.toLowerCase()} runs`}>{groupedJobs[column.id].length}</Badge>
      </header>
      <div className="grid min-w-0 gap-2 p-2">
        {groupedJobs[column.id].length ? groupedJobs[column.id].map((job) => <RunCard key={job.id} job={job} />) : <p className="px-2 py-8 text-center text-xs text-muted-foreground">No runs</p>}
      </div>
    </section>)}
  </div>;
}

function RunCard({ job }) {
  const run = currentRun(job);
  const attention = needsAttention(job.state);
  const title = jobDisplayTitle(job);
  const reference = githubIssueReference(job);
  return <Card className={cn("min-w-0 overflow-hidden", attention && "border-danger/40 bg-danger/5")}>
    <a href={`#/runs/${encodeURIComponent(job.id)}`} className="block min-w-0 p-3 transition hover:bg-muted/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50" aria-label={`Open task ${title}, ${reference || shortId(job.id)}`}>
      <div className="flex min-w-0 items-start justify-between gap-2"><p className="line-clamp-2 text-sm font-medium leading-5" title={title}>{title}</p><State value={job.state} /></div>
      <p className="mt-1 truncate font-mono text-xs text-muted-foreground" title={job.id}>{reference ? `${reference} · ` : ""}{shortId(job.id)}</p>
      <div className="mt-2 flex min-w-0 items-center gap-2 text-xs text-muted-foreground"><span className="truncate font-mono">{job.repository}</span><span>·</span><Bot className="size-3.5 shrink-0" /><span className="truncate">{job.command}</span></div>
      <div className="mt-1.5 flex min-w-0 items-center justify-between gap-3 text-xs text-muted-foreground"><span className="flex min-w-0 items-center gap-1.5"><Server className="size-3.5 shrink-0" /><span className="truncate">{run?.worker_name || "Unassigned"}</span></span><time className="shrink-0" dateTime={job.created_at}>{relativeTime(job.created_at)}</time></div>
    </a>
  </Card>;
}

function RunRow({ job }) {
  const current = currentRun(job);
  const usage = tokenUsageSummary(job.runs);
  const models = runModelSummary(job.runs);
  const title = jobDisplayTitle(job);
  const reference = githubIssueReference(job);
  return <article className="border-b border-border last:border-b-0">
    <a href={`#/runs/${encodeURIComponent(job.id)}`} className="grid w-full gap-3 px-4 py-3.5 text-left transition hover:bg-muted/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50 xl:grid-cols-[6.5rem_minmax(9rem,1.1fr)_minmax(8rem,0.9fr)_minmax(8rem,1fr)_minmax(8rem,1fr)_7rem_9rem] xl:items-center xl:gap-4" aria-label={`Open task ${title}, ${reference || shortId(job.id)}`}>
      <div className="flex items-center justify-between xl:block"><State value={job.state} /><span className="text-xs text-muted-foreground xl:hidden">{relativeTime(job.created_at)}</span></div>
      <div className="min-w-0"><p className="truncate text-sm font-medium" title={title}>{title}</p><p className="mt-1 truncate font-mono text-xs text-muted-foreground">{reference ? `${reference} · ` : ""}{shortId(job.id)}</p><p className="mt-1 break-all text-xs text-muted-foreground xl:truncate">{job.repository}</p></div>
      <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground"><Bot className="size-3.5 shrink-0" /><span className="min-w-0 flex-1 truncate text-foreground">{job.command}</span></div>
      <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground"><Server className="size-3.5 shrink-0" /><span className="truncate">{current?.worker_name || "Unassigned"}</span></div>
      <p className="min-w-0 truncate font-mono text-xs text-foreground" title={models}><span className="font-sans text-muted-foreground xl:hidden">Model · </span>{models}</p>
      <time className="hidden text-xs text-muted-foreground xl:block" dateTime={job.created_at}>{relativeTime(job.created_at)}</time>
      <div className="text-xs text-muted-foreground"><p className="font-medium tabular-nums text-foreground">{usage.total === undefined ? "Usage unavailable" : `${formatTokenUsage(usage.total)} tokens`}</p><p className="mt-0.5">{usage.unavailable ? `${usage.unavailable} unavailable · ` : ""}{job.runs.length} run{job.runs.length === 1 ? "" : "s"}</p></div>
    </a>
  </article>;
}

function State({ value }) {
  const tones = { running: "border-warning/25 bg-warning/10 text-warning", queued: "border-warning/25 bg-warning/10 text-warning", succeeded: "border-success/25 bg-success/10 text-success", failed: "border-danger/25 bg-danger/10 text-danger", timed_out: "border-danger/25 bg-danger/10 text-danger", cancelled: "border-danger/25 bg-danger/10 text-danger" };
  return <Badge className={cn("gap-1.5", tones[value] || tones.queued)}><span className="size-1.5 rounded-full bg-current" />{stateLabel(value)}</Badge>;
}

function EmptyRuns({ filtered, openComposer }) {
  return <div className="grid place-items-center px-6 py-16 text-center"><span className="grid size-10 place-items-center rounded-full bg-muted text-muted-foreground"><GitBranch className="size-5" /></span><h3 className="mt-3 text-sm font-semibold">{filtered ? "No matching runs" : "No runs yet"}</h3><p className="mt-1 max-w-sm text-xs leading-5 text-muted-foreground">{filtered ? "Try a different state filter." : "Submit a prompt to a configured command. It will appear here as soon as the control plane admits it."}</p>{!filtered && <Button variant="outline" size="sm" className="mt-4" onClick={openComposer}><Plus className="size-3.5" />New run</Button>}</div>;
}

function firstSelection(status) { return status.commands?.[0] || ""; }
function shortId(id) { const [, value = id] = id.split("_", 2); return value.slice(0, 8); }
function relativeTime(value) { if (!value || value === zeroTime) return "Not started"; const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1000)); if (seconds < 10) return "just now"; if (seconds < 60) return `${seconds}s ago`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes}m ago`; const hours = Math.floor(minutes / 60); if (hours < 24) return `${hours}h ago`; return `${Math.floor(hours / 24)}d ago`; }
function formatTimestamp(value) { return !value || value === zeroTime || !Number.isFinite(Date.parse(value)) ? "Unavailable" : new Date(value).toLocaleString(); }
function stateLabel(value) { return String(value || "unknown").replaceAll("_", " "); }
createRoot(document.getElementById("root")).render(<App />);
