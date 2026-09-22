import { Tabs } from "@/components/ui/tabs";
import { DetailPanel } from "@/components/ui/detail-panel";
import { Artifacts } from "./artifacts.jsx";
import { taskPresentation } from "./task-presentation.js";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/manrope";
import { Activity, ArrowLeft, ArrowRight, Check, BarChart3, Bot, GitBranch, LayoutDashboard, Moon, Play, Plus, Server, Sun, Table2, TimerReset, Trash2, X } from "lucide-react";
import { Analytics } from "@/analytics";
import { CommandsPage, WorkersPage } from "@/catalog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeading } from "@/components/ui/page-heading";
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
 const [title,setTitle]=useState("");
 const [sourceURL,setSourceURL]=useState("");
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
      const available = new Set(selectionChoices(next).map((choice) => choice.value));
      setSelection((current) => available.has(current) ? current : available.has(localStorage.getItem("machinist-workflow")) ? localStorage.getItem("machinist-workflow") : firstSelection(next));
      const availableRepositories = next.repositories || [];
      setRepository((current) => availableRepositories.includes(current) ? current : availableRepositories.includes(localStorage.getItem("machinist-repository")) ? localStorage.getItem("machinist-repository") : availableRepositories[0] || "");
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

  const choices = useMemo(() => selectionChoices(status), [status.commands, status.workflows]);

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
        body: JSON.stringify({ repository, model: model.trim(), ...(selection.startsWith("workflow:") ? { workflow: selection.slice(9),title: title || prompt.trim().split("\n")[0].slice(0,100),source_url:sourceURL || (/^https?:\/\/\S+$/.test(prompt.trim()) ? prompt.trim() : ""),spec:/^https?:\/\/\S+$/.test(prompt.trim()) ? "" : prompt } : { command: selection.slice(8),prompt }) }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Submission failed (${response.status})`);
      }
      localStorage.setItem("machinist-workflow",selection); localStorage.setItem("machinist-repository",repository);
      const created = await response.json();
      setPrompt(""); setTitle(""); setSourceURL("");
      setComposerOpen(false);
      await statusLoader.current.refresh();
      window.location.hash = `#/runs/${created.id}`;
    } catch (requestError) {
      setSubmitError(requestError.message);
    } finally {
      setSubmitting(false);
    }
  }

  async function workflowAction(job, action, stopped = false, feedback = "") {
    setTaskActionError("");
    try {
      const response = await fetch(`/api/v1/jobs/${encodeURIComponent(job.id)}/${action}`, {
        method: "POST", headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
        body: JSON.stringify({ run_id: job.runs.at(-1)?.id, previous_process_stopped: stopped, feedback }),
      });
      if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || "Unable to update job"); }
      await statusLoader.current.refresh();
    } catch (error) { setTaskActionError(error.message); }
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
    <div className="app-shell min-h-screen bg-background text-foreground md:flex">
      <aside className="app-sidebar sticky top-0 z-20 flex shrink-0 items-center border-b border-border bg-sidebar px-3 py-2 md:h-screen md:w-56 md:flex-col md:items-stretch md:border-b-0 md:border-r md:px-4 md:py-5">
        <div className="brand-lockup flex h-10 items-center gap-3 px-1">
          <MachinistMark />
          <span className="brand-wordmark">machinist</span>
        </div>
        <nav className="ml-4 flex flex-1 gap-1 overflow-x-auto md:ml-0 md:mt-9 md:block md:overflow-visible" aria-label="Primary">
          <a href="#/runs" aria-current={view === "runs" || view === "task" ? "page" : undefined} className={cn("nav-item", (view === "runs" || view === "task") && "nav-item-active")}><Activity className="size-4" /><span>Tasks</span><span className="ml-auto text-xs text-muted-foreground">{counts.all}</span></a>
          <a href="#/analytics" aria-current={view === "analytics" ? "page" : undefined} className={cn("nav-item", view === "analytics" && "nav-item-active")}><BarChart3 className="size-4" /><span>Analytics</span></a>
          <a href="#/workers" aria-current={view === "workers" ? "page" : undefined} className={cn("nav-item", view === "workers" && "nav-item-active")}><Server className="size-4" /><span>Workers</span></a>
          <a href="#/triggers" aria-current={view === "triggers" ? "page" : undefined} className={cn("nav-item", view === "triggers" && "nav-item-active")}><TimerReset className="size-4" /><span>Triggers</span><span className="ml-auto text-xs text-muted-foreground">{status.triggers?.length || 0}</span></a>
          <a href="#/workflows" aria-current={["commands", "workflows"].includes(view) ? "page" : undefined} className={cn("nav-item", ["commands", "workflows"].includes(view) && "nav-item-active")}><Bot className="size-4" /><span>Workflows</span></a>
        </nav>
        <div className="hidden border-t border-border pt-3 md:block">
          <div className="nav-item" title={`${connectedWorkers} connected · ${status.workers.length} registered`}><Server className="size-4 shrink-0" /><span className="min-w-0 truncate whitespace-nowrap">{connectedWorkers ? `${connectedWorkers} worker${connectedWorkers === 1 ? "" : "s"} online` : "No workers online"}</span></div>
          <button onClick={() => setDark((value) => !value)} className="nav-item w-full" aria-label={`Switch to ${dark ? "light" : "dark"} theme`}>
            {dark ? <Moon className="size-4" /> : <Sun className="size-4" />}<span>{dark ? "Dark" : "Light"} theme</span>
          </button>
        </div>
        <button onClick={() => setDark((value) => !value)} className="mobile-theme ml-auto grid size-9 place-items-center text-muted-foreground md:hidden" aria-label={`Switch to ${dark ? "light" : "dark"} theme`}>
          {dark ? <Moon className="size-4" /> : <Sun className="size-4" />}
        </button>
      </aside>

      <main className="workshop min-w-0 flex-1">
        {view === "task" ? <TaskDetail csrfToken={status.csrf_token} job={selectedJob} loaded={statusLoaded} error={statusError || taskActionError} deleting={deletingJob === route.jobID} onDelete={deleteJob} onWorkflowAction={workflowAction} /> : view === "analytics" ? <Analytics jobs={status.jobs} loaded={statusLoaded} error={statusError} /> : view === "workers" ? <WorkersPage workers={status.workers} loaded={statusLoaded} error={statusError} /> : view === "triggers" ? <TriggersPage triggers={status.triggers || []} loaded={statusLoaded} error={statusError} /> : ["commands", "workflows"].includes(view) ? <CommandsPage /> : <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
          <PageHeading title="Tasks" description="Describe the work. Review the result.">
            <div className="flex items-center gap-2">
              <Button className="text-xs!" onClick={() => setComposerOpen(true)}><Plus className="size-4" />New task</Button>
            </div>
          </PageHeading>

          {composerOpen && <RunComposer title={title} setTitle={setTitle} sourceURL={sourceURL} setSourceURL={setSourceURL} choices={choices} repositories={repositories} selection={selection} setSelection={setSelection} repository={repository} setRepository={setRepository} prompt={prompt} setPrompt={setPrompt} model={model} setModel={setModel} submitting={submitting} submit={submit} close={() => setComposerOpen(false)} />}
          {(statusError || submitError) && <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{submitError || statusError}</div>}

          <section>
            <div className="mb-3 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Filter runs">
                {[["all", "All"], ["active", "Active"], ["failed", "Failed"], ["succeeded", "Succeeded"]].map(([value, label]) => (
                  <Button key={value} variant="ghost" size="sm" aria-pressed={filter === value} onClick={() => setFilter(value)} className={cn("text-xs!", filter === value && "bg-muted text-foreground")}>{label}<span className="text-muted-foreground">{counts[value]}</span></Button>
                ))}
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3 lg:justify-end">
                <p className="text-xs text-muted-foreground">{counts.all} task{counts.all === 1 ? "" : "s"}</p>
                <div className="inline-flex rounded-lg bg-muted/60 p-1" role="group" aria-label="Runs view">
                  <Button variant="ghost" size="sm" className={cn("h-7 border-transparent px-2.5 text-xs!", runsView === "board" && "bg-surface text-foreground shadow-xs")} aria-pressed={runsView === "board"} onClick={() => setRunsView("board")}><LayoutDashboard className="size-3.5" />Board</Button>
                  <Button variant="ghost" size="sm" className={cn("h-7 border-transparent px-2.5 text-xs!", runsView === "table" && "bg-surface text-foreground shadow-xs")} aria-pressed={runsView === "table"} onClick={() => setRunsView("table")}><Table2 className="size-3.5" />List</Button>
                </div>
              </div>
            </div>

            {runsView === "board" ? <RunBoard jobs={visibleJobs} /> : <Card className="overflow-hidden">
              {visibleJobs.length ? visibleJobs.map((job) => <RunRow key={job.id} job={job} />) : <EmptyRuns filtered={filter !== "all"} openComposer={() => setComposerOpen(true)} />}
            </Card>}
          </section>

        </div>}
      </main>
    </div>
  );
}

function TaskDetail({ csrfToken, job, loaded, error, deleting, onDelete, onWorkflowAction }) {
  if (!job) return <div className="p-8"><a href="#/runs" className="text-sm underline">Back to tasks</a><p className="mt-4">{!loaded ? "Loading task…" : "Task not found."}</p>{error && <p role="alert">{error}</p>}</div>;
  const terminal=["succeeded","failed"].includes(job.state);
  const latest=job.runs.at(-1);
  const { result, history, stages } = taskPresentation(job);
  const reviewing = job.state === "awaiting_approval";
  return <div className="mx-auto max-w-[1000px] space-y-7 p-4 sm:p-6 lg:p-8">
    <header className="space-y-4"><Button asChild variant="ghost" size="sm" className="-ml-3"><a href="#/runs"><ArrowLeft className="size-4" />Back to tasks</a></Button>
      <div className="flex flex-wrap items-start justify-between gap-3"><h1 className="min-w-0 break-words text-2xl font-semibold">{jobDisplayTitle(job)}</h1><State value={job.state} /></div>
      <p className="text-sm text-muted-foreground">{job.repository} · {friendlyName(job.workflow?.name || job.command)}</p>
      {job.task?.source_url && <a className="block text-sm text-primary underline" href={job.task.source_url} target="_blank" rel="noreferrer">Original issue ↗</a>}
      {error && <p role="alert" className="text-sm text-danger">{error}</p>}
    </header>
    {stages.length > 1 && <ol className="flex flex-wrap items-center gap-3 text-sm" aria-label="Task progress">{stages.map((stage,index)=><li key={index} className="flex items-center gap-3" aria-current={stage.current ? "step" : undefined}>
      {index > 0 && <ArrowRight className="size-4 text-muted-foreground" aria-hidden="true" />}
      <span className={cn("flex items-center gap-2 py-1", stage.current ? "font-medium text-foreground" : "text-muted-foreground")}>
        {stage.complete ? <Check className="size-4 text-success" aria-label="Complete" /> : <span className="text-xs">{index+1}</span>}{friendlyName(stage.name)}
        {stage.current && <span className="text-xs text-muted-foreground">{reviewing ? "Awaiting approval" : stateLabel(job.state)}</span>}
      </span>
    </li>)}</ol>}
    <Tabs key={job.id} label="Task sections" items={[
      { id: "result", label: "Result", content: (    <Card className="space-y-5 p-5 sm:p-6" aria-label="Current result">
      <h2 className="text-lg font-semibold">{reviewing ? result ? `${friendlyName(result.command)} ready for review` : `Ready to start ${friendlyName(latest?.command).toLowerCase()}` : job.state === "running" ? `${friendlyName(latest?.command)} in progress` : job.state === "queued" ? `${friendlyName(latest?.command)} queued` : job.state === "succeeded" ? "Task complete" : `${friendlyName(latest?.command)} · ${stateLabel(job.state)}`}</h2>
      {result?.summary && <p className="line-clamp-3 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{result.summary}</p>}
      {result?.error && result.error!==result.summary && <p role="alert" className="whitespace-pre-wrap break-words text-sm text-danger">{result.error}</p>}
      {result && job.task && <Artifacts key={result.id} job={job} runID={result.id} csrfToken={csrfToken} />}
      {job.workflow && <WorkflowProgress key={`${job.id}:${latest?.id}:${job.state}`} job={job} result={result} onAction={onWorkflowAction} />}
    </Card>) },
      ...(job.task && job.runs.some(r=>r.outcome === "complete") ? [{ id: "files", label: "Files", content: <Artifacts job={job} runID={job.runs.findLast(r=>r.outcome === "complete").id} csrfToken={csrfToken} /> }] : []),
      ...(history.length ? [{ id: "history", label: "History", content: (<ol className="space-y-4">{history.map(run=><li key={run.id} className="space-y-3 border-l-2 border-border pl-4">
        <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-medium">{run.outcome === "changes_requested" ? "Changes requested" : friendlyName(run.command)}</h3><State value={run.outcome === "complete" ? "succeeded" : run.outcome || run.state} /></div>
        {run.summary && <p className="whitespace-pre-wrap leading-6">{run.summary}</p>}
        {run.error && run.error!==run.summary && <p className="text-danger">{run.error}</p>}
        {job.task && <Artifacts job={job} runID={run.id} csrfToken={csrfToken} />}
        <ExecutionDetails run={run} />
      </li>)}</ol>) }] : []),
      { id: "instructions", label: "Instructions", content: (<pre className="whitespace-pre-wrap break-words font-sans leading-6">{job.task ? job.task.spec || "Use the linked source for requirements." : job.prompt}</pre>) },
      { id: "details", label: "Details", content: <div className="space-y-6 text-sm">
        {result?.revision && <section><h2 className="mb-2 font-medium">Requested changes</h2><p className="whitespace-pre-wrap">{result.revision.feedback}</p></section>}
        {result?.summary && <section><h2 className="mb-2 font-medium">Full summary</h2><p className="whitespace-pre-wrap leading-6">{result.summary}</p></section>}
        {result && <ExecutionDetails run={result} />}
        <section className="border-t border-border pt-4"><dl className="my-4 grid gap-3 sm:grid-cols-3"><RunMetric label="Task ID" value={job.id} /><RunMetric label="Repository" value={job.repository} /><RunMetric label="Created" value={formatTimestamp(job.created_at)} /><RunMetric label="Updated" value={formatTimestamp(job.updated_at)} /></dl><Button variant="outline" disabled={!terminal || deleting} onClick={()=>onDelete(job)}>{deleting ? "Deleting…" : "Delete task"}</Button></section>
      </div> },
    ]} />
  </div>;
}

function ExecutionDetails({ run }) { return <section aria-label="execution details" className="text-xs text-muted-foreground"><dl className="mt-3 grid gap-3 sm:grid-cols-3"><RunMetric label="Started" value={formatTimestamp(run.started_at)} /><RunMetric label="Completed" value={formatTimestamp(run.completed_at)} /><RunMetric label="Exit code" value={run.exit_code === undefined ? "Unavailable" : String(run.exit_code)} /><RunMetric label="Run ID" value={run.id} mono /><RunMetric label="Executor" value={run.executor} /><RunMetric label="Worker" value={run.worker_name || "Unassigned"} /><RunMetric label="Duration" value={Number.isSafeInteger(run.duration_millis) ? formatDurationMillis(run.duration_millis) : "Not available"} /><RunMetric label="Model" value={run.model || "Executor default"} /><RunMetric label="Tokens" value={formatTokenUsage(run.token_usage) === "Unavailable" ? "Not reported" : formatTokenUsage(run.token_usage)} /></dl></section>; }

function DetailMetric({ label, value, mono = false }) { return <div className="min-w-0 border-b border-border py-3 last:border-b-0 sm:border-r sm:px-4 sm:first:pl-0 lg:border-b-0"><dt className="text-xs text-muted-foreground">{label}</dt><dd className={cn("mt-1 truncate text-sm font-medium", mono && "font-mono")} title={value}>{value}</dd></div>; }
function RunMetric({ label, value, mono = false }) { return <div className="min-w-0"><dt className="text-xs text-muted-foreground">{label}</dt><dd className={cn("mt-0.5 truncate text-sm", mono && "font-mono")} title={value}>{value}</dd></div>; }

function RunComposer({ title,setTitle,sourceURL,setSourceURL,choices,repositories,selection,setSelection,repository,setRepository,prompt,setPrompt,model,setModel,submitting,submit,close }) {
  const specHintID=React.useId();
  const isTask=selection.startsWith("workflow:");
  return <Card className="overflow-hidden border-primary/25">
    <div className="flex items-center justify-between border-b border-border px-5 py-3"><h2 className="text-sm font-semibold">New task</h2><Button variant="ghost" size="icon" onClick={close} aria-label="Close new task form"><X className="size-4" /></Button></div>
    <form onSubmit={submit} className="space-y-5 p-5">
      <label className="block space-y-2"><span className="text-sm font-medium">What do you want done?</span><textarea autoFocus aria-describedby={specHintID} className="field-control min-h-32 resize-y" value={prompt} onChange={e=>setPrompt(e.target.value)} placeholder="Describe a change or paste an issue link…" required={!sourceURL.trim() || !isTask} /></label>
      <p id={specHintID} className="text-xs leading-5 text-muted-foreground">{isTask ? <>This is your task’s spec: describe what to build and what counts as done. Prompt templates reference it as <code>{"{{task.spec}}"}</code>. A link on its own is saved as the source instead. <a href="#/workflows" className="text-primary underline">Template variables</a></> : "Write the instructions for this command. Its prompt template receives them as {{machinist.prompt}}."}</p>
      <section className="text-sm">
        <div className="grid gap-4 sm:grid-cols-2">
          <label><span className="field-label">{isTask ? "Workflow" : "Command"}</span><select className="field-control" value={selection} onChange={e=>setSelection(e.target.value)} required>{choices.map(c=><option key={c.value} value={c.value}>{c.label}</option>)}</select></label>
          <label><span className="field-label">Repository</span><select className="field-control" value={repository} onChange={e=>setRepository(e.target.value)} required>{!repositories.length && <option value="">No repositories available</option>}{repositories.map(r=><option key={r} value={r}>{r}</option>)}</select></label>
          {isTask && <><label><span className="field-label">Title · optional</span><input className="field-control" value={title} onChange={e=>setTitle(e.target.value)} maxLength={512} placeholder="From your instructions by default" /><span className="mt-1 block text-xs text-muted-foreground">Template: <code>{"{{task.title}}"}</code></span></label><label><span className="field-label">Source link · optional</span><input type="url" className="field-control" value={sourceURL} onChange={e=>setSourceURL(e.target.value)} placeholder="https://github.com/…" /><span className="mt-1 block text-xs text-muted-foreground">Template: <code>{"{{task.source_url}}"}</code></span></label></>}
          <label><span className="field-label">Model · optional</span><input className="field-control" value={model} onChange={e=>setModel(e.target.value)} maxLength={128} placeholder="Workflow default" /></label>
        </div>
      </section>
      {!choices.length && <p role="alert" className="text-sm text-danger">Configure a workflow before starting a task.</p>}
      <div className="flex justify-end"><Button disabled={submitting || !selection || !repository}>{submitting ? "Starting…" : "Start task"}<Play className="size-3.5" /></Button></div>
    </form>
  </Card>;
}

function RunBoard({ jobs }) {
  const groupedJobs = groupJobsByBoardColumn(jobs);
  return <div className="grid min-w-0 gap-4 md:grid-cols-2 xl:grid-cols-4">
    {boardColumns.map((column) => <section key={column.id} className="run-column min-w-0 border border-border bg-muted/20" aria-labelledby={`board-${column.id}`}>
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
  const title = jobDisplayTitle(job);
  const run = currentRun(job);
  return <Card className="overflow-hidden"><a href={`#/runs/${encodeURIComponent(job.id)}`} className="block min-w-0 space-y-3 p-4 transition hover:bg-muted/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50" aria-label={`Open task ${title}`}>
    <p className="line-clamp-2 text-sm font-medium leading-5">{title}</p>
    <p className="text-xs text-muted-foreground">{job.repository} · {friendlyName(run?.command || job.command)}</p>
    <State value={job.state} />
  </a></Card>;
}

function RunRow({ job }) {
  const title=jobDisplayTitle(job);
  const run=currentRun(job);
  return <article className="border-b border-border last:border-b-0"><a href={`#/runs/${encodeURIComponent(job.id)}`} className="flex items-center justify-between gap-4 px-5 py-4 hover:bg-muted/35" aria-label={`Open task ${title}`}><div className="min-w-0"><p className="truncate text-sm font-medium">{title}</p><p className="mt-1 text-xs text-muted-foreground">{job.repository} · {friendlyName(run?.command || job.command)}</p></div><div className="flex shrink-0 flex-col items-end gap-1"><State value={job.state} /><time className="text-xs text-muted-foreground" dateTime={job.created_at}>{relativeTime(job.created_at)}</time></div></a></article>;
}

function State({ value }) {
  const tones = { running: "border-warning/25 bg-warning/10 text-warning", queued: "border-warning/25 bg-warning/10 text-warning", succeeded: "border-success/25 bg-success/10 text-success", failed: "border-danger/25 bg-danger/10 text-danger", timed_out: "border-danger/25 bg-danger/10 text-danger", cancelled: "border-danger/25 bg-danger/10 text-danger" };
  return <Badge className={cn("gap-1.5", tones[value] || tones.queued)}><span className="size-1.5 rounded-full bg-current" />{stateLabel(value)}</Badge>;
}

function EmptyRuns({ filtered, openComposer }) {
  return <div className="grid place-items-center px-6 py-16 text-center"><span className="grid size-10 place-items-center rounded-full bg-muted text-muted-foreground"><GitBranch className="size-5" /></span><h3 className="mt-3 text-sm font-semibold">{filtered ? "No matching tasks" : "No tasks yet"}</h3><p className="mt-1 max-w-sm text-xs leading-5 text-muted-foreground">{filtered ? "Try a different state filter." : "Describe the work or paste an issue link to get started."}</p>{!filtered && <Button variant="outline" size="sm" className="mt-4" onClick={openComposer}><Plus className="size-3.5" />New task</Button>}</div>;
}

function MachinistMark() {
  return <svg className="machinist-mark" viewBox="0 0 64 48" aria-hidden="true">
    <path className="machinist-mark-piece-a" d="M8 40V18C8 11 12 7 18 7s10 4 10 11v10h4" />
    <path className="machinist-mark-piece-b" d="M32 28h4V18c0-7 4-11 10-11s10 4 10 11v22" />
  </svg>;
}

function friendlyName(name) { return String(name || "").replaceAll("_", " ").replaceAll("-", " ").replace(/^./,c=>c.toUpperCase()); }
function selectionChoices(status) {
 const workflows=status.workflows || [];
 return workflows.length ? workflows.map(name=>({value:`workflow:${name}`,label:friendlyName(name)})) : (status.commands || []).map(name=>({value:`command:${name}`,label:friendlyName(name)}));
}
function firstSelection(status) { return selectionChoices(status)[0]?.value || ""; }
function shortId(id) { const [, value = id] = id.split("_", 2); return value.slice(0, 8); }
function relativeTime(value) { if (!value || value === zeroTime) return "Not started"; const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1000)); if (seconds < 10) return "just now"; if (seconds < 60) return `${seconds}s ago`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes}m ago`; const hours = Math.floor(minutes / 60); if (hours < 24) return `${hours}h ago`; return `${Math.floor(hours / 24)}d ago`; }
function formatTimestamp(value) { return !value || value === zeroTime || !Number.isFinite(Date.parse(value)) ? "Unavailable" : new Date(value).toLocaleString(); }
function stateLabel(value) { return String(value || "unknown").replaceAll("_", " "); }
createRoot(document.getElementById("root")).render(<App />);

function WorkflowProgress({ job, result, onAction }) {
 const [stopped, setStopped] = useState(false);
 const [requesting,setRequesting]=useState(false);
 const [feedback,setFeedback]=useState("");
 const [busy, setBusy] = useState(false);
 const latest = job.runs.at(-1);
 const prURL = (result || latest)?.summary?.match(/https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/pull\/\d+/)?.[0];
 const action = async (name) => { setBusy(true); try { await onAction(job, name, stopped, name === "request_changes" ? feedback : ""); } finally { setBusy(false); } };
 const retry = ["blocked", "failed", "interrupted", "cancelled"].includes(job.state);
 return <div className="space-y-3">
  {prURL && <Button asChild variant="outline"><a href={prURL} target="_blank" rel="noreferrer">Open PR</a></Button>}
  {job.state === "awaiting_approval" && <div className="space-y-3"><div className="flex flex-wrap gap-2"><Button disabled={busy || requesting} onClick={() => action("approve")}>Approve and start {friendlyName(latest?.command).toLowerCase()}</Button>{latest?.reviewed_run_id && <Button variant="outline" disabled={busy} onClick={()=>setRequesting(true)}>Request changes</Button>}</div>{requesting && <div className="space-y-3"><label className="block"><span className="field-label">What needs to change?</span><textarea className="field-control min-h-24" value={feedback} onChange={e=>setFeedback(e.target.value)} maxLength={4000} placeholder="Explain what to revise in the previous stage’s result." /></label><p className="text-xs text-muted-foreground">You’ll review the revised result before continuing.</p><div className="flex flex-wrap gap-2"><Button disabled={busy || !feedback.trim()} onClick={()=>action("request_changes")}>{busy ? "Submitting…" : "Send feedback and revise"}</Button><Button variant="ghost" disabled={busy} onClick={()=>setRequesting(false)}>Keep reviewing</Button></div></div>}</div>}
  {job.state === "blocked" && <p className="text-sm text-muted-foreground">Update the issue or resolve the blocker, then retry this step.</p>}
  {["interrupted", "cancelled"].includes(job.state) && <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={stopped} onChange={(event) => setStopped(event.target.checked)} />I have verified the previous worker process has stopped. Retrying will inspect existing work before continuing.</label>}
  {["queued", "running", "awaiting_approval", "blocked"].includes(job.state) && <Button variant="ghost" className="text-muted-foreground hover:text-danger" disabled={busy} onClick={() => action("cancel")}>Cancel task</Button>}
  {retry && <Button disabled={busy || (["interrupted", "cancelled"].includes(job.state) && !stopped)} onClick={() => action("retry")}>Retry {friendlyName(latest?.command).toLowerCase()}</Button>}
 </div>;
}
