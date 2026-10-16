import { Onboarding } from "./onboarding.jsx";
import { TaskDetail } from "./task-detail.jsx";
import { StageProgress, stageLabel } from "./stage-progress.jsx";
import { State, friendlyName } from "./task-display.jsx";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import { Activity, LayoutDashboard, Bot, Clock3, Settings, Search, GitBranch, Moon, Play, Plus, Server, Sun, X } from "lucide-react";
import { Dashboard } from "@/analytics";
import { AgentsPage, WorkersPage } from "@/catalog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeading } from "@/components/ui/page-heading";
import { cn } from "@/lib/utils";
import { routeFromHash } from "@/routes";
import { currentRun, filterJobs, jobCounts, jobDisplayTitle } from "@/runs-board";
import { createStatusLoader } from "@/status-loader";
import { formatDurationMillis } from "./run-metrics.js";
import { ProjectsPage } from "./projects.jsx";
import { AutomationsPage } from "./automations.jsx";
import { TriggersPage } from "@/triggers";
import "./styles.css";


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
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [recipe, setRecipe] = useState("factory");
  const submission = useRef(null);
  const [dark, setDark] = useState(() => localStorage.getItem("machinist-theme") !== "light");
  const [route, setRoute] = useState(() => routeFromHash(window.location.hash));
  const previousView = useRef(route.view);
  const view = status.onboarding?.required ? "setup" : route.view;
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
      setSelection((current) => available.has(current) ? current : available.has("workflow:factory") ? "workflow:factory" : available.has(localStorage.getItem("machinist-workflow")) ? localStorage.getItem("machinist-workflow") : firstSelection(next));
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

  useEffect(() => {
    if (route.view === "runs") {
      setFilter(route.filter || "all");
      if (previousView.current !== "runs") setSearch("");
    }
    previousView.current = route.view;
  }, [route]);

  const choices = useMemo(() => selectionChoices(status), [status.commands, status.workflows]);

  const repositories = status.repositories;

  const counts = useMemo(() => jobCounts(status.jobs), [status.jobs]);
  const visibleJobs = useMemo(() => filterJobs(status.jobs, filter), [filter, status.jobs]);

  const searchedJobs = visibleJobs.filter(job => `${jobDisplayTitle(job)} ${job.repository} ${currentRun(job)?.command || ""}`.toLowerCase().includes(search.toLowerCase()));
  const connectedWorkers = status.workers.filter((worker) => worker.connected).length;
  const selectedJob = route.jobID ? status.jobs.find((job) => job.id === route.jobID) : undefined;

  async function submit(event) {
    event.preventDefault();
    setSubmitting(true);
    setSubmitError("");
    try {
      const payload = { repository, model: model.trim(), ...(selection.startsWith("workflow:") ? { workflow: selection.slice(9), title: prompt.trim().split("\n")[0].replace(/^#+\s*/, "").slice(0,100), source_url: (/^https?:\/\/\S+$/.test(prompt.trim()) ? prompt.trim() : ""), spec: /^https?:\/\/\S+$/.test(prompt.trim()) ? "" : prompt } : { command: selection.slice(8), prompt }) };
      if (["local", "github"].includes(status.state_backend)) payload.recipe = recipe;
      const fingerprint = JSON.stringify(payload);
      if (!submission.current || submission.current.fingerprint !== fingerprint) submission.current = { fingerprint, id: crypto.randomUUID() };
      const response = await fetch("/api/v1/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
        body: JSON.stringify({ ...payload, client_request_id: submission.current.id }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Submission failed (${response.status})`);
      }
      localStorage.setItem("machinist-workflow",selection); localStorage.setItem("machinist-repository",repository);
      const created = await response.json();
      setPrompt(""); submission.current = null;
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
        body: JSON.stringify({ run_id: job.runs.at(-1)?.id, previous_process_stopped: stopped, feedback, hold_version: job.hold_version || 0, expected_updated_at: job.updated_at }),
      });
      if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || "Unable to update job"); }
      await statusLoader.current.refresh();
      return true;
    } catch (error) { setTaskActionError(error.message); return false; }
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
        <div className="flex h-10 items-center px-1">
          <span className="brand-wordmark">Machinist</span>
        </div>
        <nav className="primary-nav" aria-label="Primary">
          <span className="nav-group">Work</span>
          {[["dashboard", LayoutDashboard, "Dashboard"], ["runs", Activity, "Runs"], ["automations", Clock3, "Automations"]].map(([page, Icon, label]) => <a key={page} href={`#/${page}`} aria-current={view === page || (page === "runs" && view === "task") ? "page" : undefined} className={cn("nav-item", (view === page || (page === "runs" && view === "task")) && "nav-item-active")}><Icon className="size-4" /><span>{label}</span></a>)}
          <span className="nav-group">Infrastructure</span>
          {[["repositories", GitBranch, "Projects"], ["agents", Bot, "Agents"], ["runners", Server, "Runners"]].map(([page,Icon,label]) => <a key={page} href={`#/${page}`} aria-current={view === page ? "page" : undefined} className={cn("nav-item", view === page && "nav-item-active")}><Icon className="size-4" /><span>{label}</span></a>)}
          <a href="#/settings" className={cn("nav-item settings-nav", ["settings", "workers"].includes(view) && "nav-item-active")} aria-current={view === "settings" ? "page" : undefined}><Settings className="size-4" /><span>Settings</span></a>
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
        <div className="runtime-bar" role="status"><span><i className={!statusError && connectedWorkers ? "runtime-online" : ""} />{statusError ? "Connection lost" : connectedWorkers ? "Connected" : "No runner online"}</span><span>{status.jobs.filter(job => job.state === "running").length} running</span><span>{status.jobs.filter(job => job.state === "queued").length} queued</span><span className="runtime-location">{({remote:"VM",kubernetes:"Kubernetes",local:"Local"}[status.onboarding?.selection?.host] || "Local")}</span></div>
        {view === "setup" ? <Onboarding csrfToken={status.csrf_token} onReady={() => statusLoader.current.refresh()} /> : view === "task" ? <TaskDetail csrfToken={status.csrf_token} job={selectedJob} loaded={statusLoaded} error={statusError || taskActionError} deleting={deletingJob === route.jobID} onDelete={deleteJob} onWorkflowAction={workflowAction} /> : view === "dashboard" ? <Dashboard jobs={status.jobs} workers={status.workers} automations={status.automations} loaded={statusLoaded} error={statusError} onNewRun={() => { setComposerOpen(true); window.location.hash = "/runs"; }} /> : ["runners", "workers"].includes(view) ? <WorkersPage workers={status.workers} loaded={statusLoaded} error={statusError} /> : view === "repositories" ? <ProjectsPage status={status} onRefresh={() => statusLoader.current.refresh()} /> : view === "settings" ? <div><div className="settings-connection-link"><h1>Settings</h1><a href="#/setup">GitHub connection and setup</a></div><div className="settings-links"><a href="#/repositories">Projects</a><a href="#/runners">Runners and connections</a><a href="#/automations">Automations</a><a href="#/agents">Agent prompts</a></div></div> : ["triggers", "automations"].includes(view) ? (status.state_backend === "local" ? <AutomationsPage automations={status.automations || []} jobs={status.jobs} repositories={status.repositories} csrfToken={status.csrf_token} loaded={statusLoaded} error={statusError} onRefresh={() => statusLoader.current.refresh()} /> : <TriggersPage stateBackend={status.state_backend} triggers={status.triggers || []} loaded={statusLoaded} error={statusError} />) : view === "agents" ? <AgentsPage csrfToken={status.csrf_token} repositories={status.repositories} stateBackend={status.state_backend} /> : <div className="runs-page">
          <PageHeading title="Runs" description="Agent jobs across your repositories.">
            <div className="flex items-center gap-2">
              {!composerOpen && <Button className="text-xs!" onClick={() => setComposerOpen(true)}><Plus className="size-4" />New run</Button>}
            </div>
          </PageHeading>

          {composerOpen && <RunComposer recipe={recipe} setRecipe={setRecipe} stateBackend={status.state_backend} choices={choices} repositories={repositories} selection={selection} setSelection={setSelection} repository={repository} setRepository={setRepository} prompt={prompt} setPrompt={setPrompt} model={model} setModel={setModel} submitting={submitting} submit={submit} close={() => setComposerOpen(false)} />}
          {(statusError || submitError) && <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{submitError || statusError}</div>}

          <section>
            <label className="run-search"><Search className="size-4" /><input aria-label="Search runs" placeholder="Search runs…" value={search} onChange={event => setSearch(event.target.value)} /></label>
            <div className="mb-3 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Filter runs">
                {[["all", "All"], ["active", "Active"], ["pr_ready", "PR ready"], ["attention", "Needs attention"], ["failed", "Failed"], ["succeeded", "Succeeded"]].map(([value, label]) => (
                  <Button key={value} variant="ghost" size="sm" aria-pressed={filter === value} onClick={() => { setFilter(value); window.location.hash = value === "all" ? "/runs" : `/runs?filter=${value}`; }} className={cn("text-xs!", filter === value && "bg-muted text-foreground")}>{label}<span className="text-muted-foreground">{counts[value]}</span></Button>
                ))}
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3 lg:justify-end">
                <p className="text-xs text-muted-foreground">{counts.all} task{counts.all === 1 ? "" : "s"}</p>

              </div>
            </div>

            {searchedJobs.length ? <div className="runs-table" role="table" aria-label="Runs"><div className="run-grid run-table-head" role="row">{["State", "Task", "Progress", "Stage", "Repository", "Target", "Duration", "Delivery"].map(label => <span role="columnheader" key={label}>{label}</span>)}</div>{searchedJobs.map(job => <RunRow key={job.id} job={job} />)}</div> : <EmptyRuns filtered={filter !== "all" || Boolean(search)} openComposer={() => setComposerOpen(true)} />}

          </section>

        </div>}
      </main>
    </div>
  );
}

function RunComposer({ recipe,setRecipe,stateBackend,choices,repositories,selection,repository,setRepository,prompt,setPrompt,model,setModel,submitting,submit,close }) {
  const hintID=React.useId();
  const taskMode=["local", "github"].includes(stateBackend);
  return <Card className="run-composer">
    <header className="composer-heading"><h2>New run</h2><Button variant="ghost" size="icon" onClick={close} aria-label="Close new run form"><X className="size-4" /></Button></header>
    <form onSubmit={submit} className="composer-form">
      <label><span className="field-label">What do you want done?</span><textarea autoFocus aria-describedby={hintID} className="field-control composer-prompt" value={prompt} onChange={e=>setPrompt(e.target.value)} placeholder="Describe a task or paste a GitHub issue link…" required /></label>
      <div className="composer-options"><label><span className="field-label">Repository</span><select className="field-control" value={repository} onChange={e=>setRepository(e.target.value)} required>{!repositories.length && <option value="">No repositories available</option>}{repositories.map(r=><option key={r} value={r}>{r}</option>)}</select></label>
      {taskMode && <label><span className="field-label">Run mode</span><select className="field-control" value={recipe} onChange={event => setRecipe(event.target.value)}><option value="factory">Standard pipeline</option><option value="single">Single agent</option></select></label>}
      {!taskMode && <label><span className="field-label">Model override</span><input className="field-control" value={model} onChange={e=>setModel(e.target.value)} maxLength={128} placeholder="Agent default" /></label>}</div>
      <p id={hintID} className="composer-hint">{!taskMode ? "Uses the configured agent and execution target." : recipe === "single" ? "One agent completes the task. No separate review." : "Triage → optional Spec → Build → Review. You decide when to merge."}</p>
      {!choices.length && <p role="alert" className="text-sm text-danger">Complete setup before starting a run.</p>}
      <footer><Button variant="ghost" type="button" onClick={close}>Cancel</Button><Button disabled={submitting || !selection || !repository}>{submitting ? "Starting…" : "Start run"}<Play className="size-3.5" /></Button></footer>
    </form>
  </Card>;
}

function RunRow({ job }) {
  const title = jobDisplayTitle(job);
  const run = currentRun(job);
  const progress = job.workflow?.factory;
  const duration = run?.duration_millis ?? (run?.state === "running" && run.started_at ? Math.max(0, Date.now() - Date.parse(run.started_at)) : undefined);
  const agent = run?.executor?.replace(/-review$/, "");
  return <div className="run-grid run-data-row" role="row">
    <span role="cell" className="run-state"><State value={job.state} compact /></span>
    <span role="cell"><a href={`#/runs/${encodeURIComponent(job.id)}`} className="run-title" aria-label={`Open task ${title}`}><strong>{title}</strong><small>{({single:"Single agent",direct:"Deliver",factory:"Standard",answer:"Answer"}[job.recipe] || friendlyName(job.workflow?.name || job.command))}</small></a><span className="run-mobile-progress"><StageProgress job={job} /></span></span>
    <span role="cell" className="run-progress"><StageProgress job={job} /></span>
    <span role="cell" className="run-agent">{job.recipe === "single" ? "Agent" : job.recipe === "answer" ? "Answer" : stageLabel(run?.command || job.command)}<small>{agent === "codex" ? "Codex" : agent === "claude" ? "Claude Code" : agent || "Waiting"}</small></span>
    <span role="cell" className="run-repository" title={job.repository}>{job.repository}</span>
    <span role="cell" className="run-target">{run?.worker_name || "Waiting"}</span>
    <span role="cell" className="run-duration">{duration === undefined ? "—" : formatDurationMillis(duration)}</span>
    <span role="cell" className="run-delivery">{job.publication_hold ? "Private" : progress?.pr_url ? <a href={progress.pr_url} target="_blank" rel="noreferrer">PR #{progress.pr_url.split("/").at(-1)}</a> : "—"}</span>
  </div>;
}

function EmptyRuns({ filtered, openComposer }) {
  return <div className="grid place-items-center px-6 py-16 text-center"><span className="grid size-10 place-items-center rounded-full bg-muted text-muted-foreground"><GitBranch className="size-5" /></span><h3 className="mt-3 text-sm font-semibold">{filtered ? "No matching tasks" : "No tasks yet"}</h3><p className="mt-1 max-w-sm text-xs leading-5 text-muted-foreground">{filtered ? "Try a different state filter." : "Describe the work or paste an issue link to get started."}</p>{!filtered && <Button variant="outline" size="sm" className="mt-4" onClick={openComposer}><Plus className="size-3.5" />New run</Button>}</div>;
}

function selectionChoices(status) {
 const workflows=status.workflows || [];
 return workflows.length ? workflows.map(name=>({value:`workflow:${name}`,label:friendlyName(name)})) : (status.commands || []).map(name=>({value:`command:${name}`,label:friendlyName(name)}));
}
function firstSelection(status) { return status.workflows?.includes("factory") ? "workflow:factory" : selectionChoices(status)[0]?.value || ""; }
function shortId(id) { const [, value = id] = id.split("_", 2); return value.slice(0, 8); }
export const appRoot = createRoot(document.getElementById("root"));
appRoot.render(<App />);
