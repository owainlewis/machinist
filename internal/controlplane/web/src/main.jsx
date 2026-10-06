import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";
import { ChartNoAxesColumn, CircleCheckBig, Clock3, House, LayoutGrid, List, Plus, Search, Server, Settings } from "lucide-react";
import { TaskDetail } from "./task-detail.jsx";
import { friendlyName, relativeTime, shortId } from "./task-display.jsx";
import { UsagePage } from "@/analytics";
import { WorkersPage } from "@/catalog";
import { SettingsPage } from "@/settings";
import { AutomationsPage } from "@/triggers";
import { Button } from "@/components/ui/button";
import { ErrorBanner, QuietState, TopBar } from "@/components/ui/page-heading";
import { Select } from "@/components/ui/select";
import { Spinner, StatusIcon } from "@/components/ui/status-icon";
import { cn } from "@/lib/utils";
import { routeFromHash, taskHref } from "@/routes";
import { boardColumns, currentRun, groupJobs, groupJobsByBoardColumn, jobDisplayTitle, searchJobs, statusGroup, statusGroups, taskReason } from "@/runs-board";
import { createStatusLoader } from "@/status-loader";
import "./styles.css";

function App() {
  const [status, setStatus] = useState({ jobs: [], workers: [], commands: [], repositories: [], triggers: [], csrf_token: "" });
  const [statusError, setStatusError] = useState("");
  const [statusLoaded, setStatusLoaded] = useState(false);
  const [taskActionError, setTaskActionError] = useState("");
  const [deletingJob, setDeletingJob] = useState("");
  const [dark, setDark] = useState(() => readSetting("machinist-theme") !== "light");
  const [route, setRoute] = useState(() => routeFromHash(window.location.hash));
  const composerRef = useRef(null);
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
      setStatus(result.status);
      setStatusError("");
      setStatusLoaded(true);
    },
  });

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    writeSetting("machinist-theme", dark ? "dark" : "light");
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

  // C starts a new task from anywhere, like Linear.
  useEffect(() => {
    const onKey = (event) => {
      if (event.key.toLowerCase() !== "c" || event.metaKey || event.ctrlKey || event.altKey || isTyping(event.target)) return;
      event.preventDefault();
      openComposer();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  function openComposer() {
    if (routeFromHash(window.location.hash).view !== "home") window.location.hash = "#/home";
    window.setTimeout(() => composerRef.current?.focus(), 0);
  }

  const view = route.view;
  const jobs = status.jobs;
  const attentionCount = jobs.filter((job) => ["needs", "failed"].includes(statusGroup(job.state))).length;
  const openCount = jobs.filter((job) => statusGroup(job.state) !== "done").length;
  const selectedJob = route.jobID ? jobs.find((job) => job.id === route.jobID) : undefined;

  async function submit(body) {
    const response = await fetch("/api/v1/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      const result = await response.json().catch(() => ({}));
      throw new Error(result.error || `Submission failed (${response.status})`);
    }
    const created = await response.json();
    await statusLoader.current.refresh();
    window.location.hash = taskHref(created.id);
  }

  async function workflowAction(job, action, stopped = false, feedback = "") {
    setTaskActionError("");
    try {
      const response = await fetch(`/api/v1/jobs/${encodeURIComponent(job.id)}/${action}`, {
        method: "POST", headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
        body: JSON.stringify({ run_id: job.runs.at(-1)?.id, previous_process_stopped: stopped, feedback }),
      });
      if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || "Unable to update task"); }
      await statusLoader.current.refresh();
    } catch (error) { setTaskActionError(error.message); }
  }

  async function deleteJob(job) {
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
      window.location.hash = "#/tasks";
    } catch (requestError) {
      setTaskActionError(requestError.message);
    } finally {
      setDeletingJob("");
    }
  }

  const nav = [
    ["home", "Home", House, attentionCount ? <span className="nav-count nav-count-attention" aria-label={`${attentionCount} need you`}>{attentionCount}</span> : null],
    ["tasks", "Tasks", CircleCheckBig, <span className="nav-count">{openCount || ""}</span>],
    ["automations", "Automations", Clock3, null],
    ["workers", "Workers", Server, null],
    ["usage", "Usage", ChartNoAxesColumn, null, "desktop-only"],
    ["settings", "Settings", Settings, null, "mobile-only"],
  ];
  const current = view === "task" ? "tasks" : view;

  return (
    <div className="app-shell">
      <aside className="app-sidebar">
        <div className="brand-wordmark">Machinist</div>
        <nav aria-label="Primary">
          {nav.map(([id, label, Icon, count, extra]) => <a key={id} href={`#/${id}`} aria-current={current === id ? "page" : undefined} className={cn("nav-item", extra)}><Icon className="size-3.5" /><span>{label}</span>{count}</a>)}
        </nav>
        <div className="sidebar-footer">
          <WorkerHealth workers={status.workers} jobs={jobs} />
          <a href="#/settings" aria-current={current === "settings" ? "page" : undefined} className="nav-item"><Settings className="size-3.5" /><span>Settings</span></a>
        </div>
      </aside>

      <main className="pane">
        {view === "task" ? <TaskDetail csrfToken={status.csrf_token} job={selectedJob} workers={status.workers} loaded={statusLoaded} error={statusError || taskActionError} deleting={deletingJob === route.jobID} onDelete={deleteJob} onWorkflowAction={workflowAction} />
          : view === "tasks" ? <TasksPage jobs={jobs} workers={status.workers} loaded={statusLoaded} error={statusError} onNew={openComposer} />
          : view === "automations" ? <AutomationsPage triggers={status.triggers || []} jobs={jobs} loaded={statusLoaded} error={statusError} />
          : view === "workers" ? <WorkersPage workers={status.workers} jobs={jobs} loaded={statusLoaded} error={statusError} />
          : view === "usage" ? <UsagePage jobs={jobs} loaded={statusLoaded} error={statusError} />
          : view === "settings" ? <SettingsPage status={status} loaded={statusLoaded} error={statusError} dark={dark} setDark={setDark} />
          : <HomePage status={status} loaded={statusLoaded} error={statusError} submit={submit} composerRef={composerRef} />}
      </main>
    </div>
  );
}

function WorkerHealth({ workers, jobs }) {
  const online = workers.filter((worker) => worker.connected);
  const running = jobs.filter((job) => job.state === "running").length;
  return <a href="#/workers" className="worker-health" title={`${online.length} connected · ${workers.length} registered`}>
    <StatusIcon state={online.length ? "online" : "offline"} />
    <span className="min-w-0 truncate whitespace-nowrap">{online.length ? `${online.length} worker${online.length === 1 ? "" : "s"} online` : "No workers online"}{running ? ` · ${running} running` : ""}</span>
  </a>;
}

function HomePage({ status, loaded, error, submit, composerRef }) {
  const jobs = status.jobs;
  const needs = jobs.filter((job) => ["needs", "failed"].includes(statusGroup(job.state)));
  const live = [...jobs.filter((job) => job.state === "running"), ...jobs.filter((job) => job.state === "queued")];
  return <>
    <TopBar title="Home" />
    {error && <ErrorBanner>{error}</ErrorBanner>}
    <div className="pane-scroll">
      <div className="mx-auto flex max-w-3xl flex-col gap-7 px-4 py-8 sm:px-6 sm:py-12">
        <Composer status={status} submit={submit} inputRef={composerRef} />
        <HomeSection title="Needs you" jobs={needs} workers={status.workers} loaded={loaded} empty={<><StatusIcon state="done" />Nothing needs you.</>} />
        <HomeSection title="In progress" jobs={live} workers={status.workers} loaded={loaded} empty="Nothing running." />
      </div>
    </div>
  </>;
}

function HomeSection({ title, jobs, workers, loaded, empty }) {
  return <section aria-label={title}>
    <h2 className="mb-1 flex gap-1.5 text-[0.78125rem] font-medium text-muted-foreground">{title}<span className="text-faint">{loaded ? jobs.length : ""}</span></h2>
    {!loaded ? <div className="flex items-center gap-2 border-b border-border py-3 text-faint"><Spinner />Loading</div>
      : jobs.length ? jobs.map((job) => <TaskRow key={job.id} job={job} workers={workers} compact />)
      : <div className="flex items-center gap-2 border-b border-border py-3 text-faint">{empty}</div>}
  </section>;
}

function Composer({ status, submit, inputRef }) {
  const choices = useMemo(() => selectionChoices(status), [status.commands, status.workflows]);
  const repositories = status.repositories || [];
  const [text, setText] = useState("");
  const [selection, setSelection] = useState("");
  const [repository, setRepository] = useState("");
  const [title, setTitle] = useState("");
  const [model, setModel] = useState("");
  const [options, setOptions] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  // Keep the last choice when it is still configured; otherwise fall back to the first.
  const values = choices.map((choice) => choice.value);
  const chosen = pick(values, selection, readSetting("machinist-workflow"));
  const repo = pick(repositories, repository, readSetting("machinist-repository"));
  const isWorkflow = chosen.startsWith("workflow:");
  const ready = text.trim() && chosen && repo && !submitting;

  async function onSubmit(event) {
    event?.preventDefault();
    if (!ready) return;
    setSubmitting(true);
    setError("");
    const value = text.trim();
    const link = /^https?:\/\/\S+$/.test(value) ? value : "";
    try {
      await submit({
        repository: repo, model: model.trim(),
        ...(isWorkflow
          ? { workflow: chosen.slice(9), title: title.trim() || value.split("\n")[0].slice(0, 100), source_url: link, spec: link ? "" : text }
          : { command: chosen.slice(8), prompt: text }),
      });
      writeSetting("machinist-workflow", chosen);
      writeSetting("machinist-repository", repo);
      setText(""); setTitle(""); setModel(""); setOptions(false);
    } catch (requestError) {
      setError(requestError.message);
    } finally {
      setSubmitting(false);
    }
  }

  return <form onSubmit={onSubmit} aria-label="New task">
    <div className="composer">
      <textarea ref={inputRef} className="composer-input" rows={3} value={text} onChange={(event) => setText(event.target.value)}
        onKeyDown={(event) => { if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) onSubmit(event); }}
        placeholder="Paste an issue URL or describe what to build" aria-label="What should the factory do?" />
      {options && <div className="grid gap-3 px-3.5 pb-1 sm:grid-cols-2">
        {isWorkflow && <label><span className="field-label">Title</span><input className="field-control" value={title} onChange={(event) => setTitle(event.target.value)} maxLength={512} placeholder="From the first line by default" /></label>}
        <label><span className="field-label">Model</span><input className="field-control" value={model} onChange={(event) => setModel(event.target.value)} maxLength={128} placeholder="Agent default" /></label>
      </div>}
      <div className="flex flex-wrap items-center gap-1.5 px-2.5 py-2">
        <Select label="Repository" value={repo} onValueChange={setRepository} items={repositories.map((name) => ({ value: name, label: name }))} placeholder="No repositories" disabled={!repositories.length} />
        <Select label={isWorkflow ? "Workflow" : "Agent"} value={chosen} onValueChange={setSelection} items={choices} placeholder="No agents configured" disabled={!choices.length} />
        <Button type="button" variant="ghost" size="sm" aria-pressed={options} onClick={() => setOptions((value) => !value)}>Options</Button>
        <span className="flex-1" />
        <Button type="submit" disabled={!ready}>{submitting ? <><Spinner label="Starting" />Starting</> : <>Start task<span className="kbd border-background/30 text-background/70">⌘↵</span></>}</Button>
      </div>
    </div>
    {error && <p role="alert" className="mt-2 text-danger">{error}</p>}
  </form>;
}

function TasksPage({ jobs, workers, loaded, error, onNew }) {
  const [query, setQuery] = useState("");
  const [layout, setLayout] = useState(() => readSetting("machinist-tasks-layout") === "board" ? "board" : "list");
  useEffect(() => writeSetting("machinist-tasks-layout", layout), [layout]);
  const visible = useMemo(() => searchJobs(jobs, query), [jobs, query]);
  return <>
    <TopBar title="Tasks">
      <label className="relative hidden sm:block"><Search className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-faint" /><input className="compact-select w-44 pl-7 text-foreground" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search" aria-label="Search tasks" /></label>
      <div className="flex rounded-md border border-border-strong p-px" role="group" aria-label="Layout">
        <Button variant="ghost" size="sm" className={cn("h-[1.375rem]", layout === "list" && "bg-selected text-foreground")} aria-pressed={layout === "list"} onClick={() => setLayout("list")}><List className="size-3.5" />List</Button>
        <Button variant="ghost" size="sm" className={cn("h-[1.375rem]", layout === "board" && "bg-selected text-foreground")} aria-pressed={layout === "board"} onClick={() => setLayout("board")}><LayoutGrid className="size-3.5" />Board</Button>
      </div>
      <Button variant="outline" onClick={onNew}><Plus className="size-3.5" />New task<span className="kbd desktop-only">C</span></Button>
    </TopBar>
    {error && <ErrorBanner>{error}</ErrorBanner>}
    <div className="pane-scroll">
      {!loaded ? <QuietState title="Loading tasks" loading role="status" />
        : !jobs.length ? <QuietState title="No tasks yet" description="Press C or use New task to start one." />
        : !visible.length ? <QuietState title="No tasks match" description="Try a different search." />
        : layout === "board" ? <TaskBoard jobs={visible} workers={workers} /> : <TaskList jobs={visible} workers={workers} />}
    </div>
  </>;
}

function TaskList({ jobs, workers }) {
  const groups = groupJobs(jobs);
  return statusGroups.filter((group) => groups[group.id]?.length).map((group) => <section key={group.id} aria-label={group.title}>
    <h2 className="group-header"><StatusIcon state={group.id} />{group.title}<span className="font-normal text-faint">{groups[group.id].length}</span></h2>
    {groups[group.id].map((job) => <TaskRow key={job.id} job={job} workers={workers} />)}
  </section>);
}

function TaskBoard({ jobs, workers }) {
  const columns = groupJobsByBoardColumn(jobs);
  return <div className="grid min-w-0 gap-2 p-3 md:grid-cols-2 xl:grid-cols-4">
    {boardColumns.map((column) => <section key={column.id} className="min-w-0" aria-labelledby={`board-${column.id}`}>
      <h2 id={`board-${column.id}`} className="mx-1 mb-2 flex items-center gap-2 text-[0.78125rem] font-medium"><StatusIcon state={column.id} />{column.title}<span className="font-normal text-faint">{columns[column.id].length}</span></h2>
      <div className="grid gap-1.5">
        {columns[column.id].length ? columns[column.id].map((job) => <TaskCard key={job.id} job={job} workers={workers} />) : <p className="rounded-md border border-dashed border-border-strong px-2 py-6 text-center text-xs text-faint">None</p>}
      </div>
    </section>)}
  </div>;
}

function TaskRow({ job, workers, compact = false }) {
  const title = jobDisplayTitle(job);
  const reason = taskReason(job, workers);
  return <a href={taskHref(job.id)} className={cn("row", compact && "px-1")} aria-label={`Open task ${title}`}>
    {!compact && <span className="row-id w-16 flex-none font-mono text-[0.6875rem] text-faint">{shortId(job.id)}</span>}
    <StatusIcon state={job.state} />
    <span className="row-main"><span className="row-title">{title}</span><span className={cn("row-reason", reason.tone && `tone-${reason.tone}`)}>{reason.text}</span></span>
    <span className="row-meta"><span className="desktop-only">{job.repository}</span><TaskTime job={job} /></span>
  </a>;
}

function TaskCard({ job, workers }) {
  const title = jobDisplayTitle(job);
  const reason = taskReason(job, workers);
  return <a href={taskHref(job.id)} className="flex min-w-0 flex-col gap-2 rounded-md border border-border bg-muted p-2.5 hover:border-border-strong hover:bg-selected" aria-label={`Open task ${title}`}>
    <span className="line-clamp-2 leading-5">{title}</span>
    <span className={cn("line-clamp-2 text-xs", reason.tone ? `tone-${reason.tone}` : "text-faint")}>{reason.text}</span>
    <span className="flex items-center gap-2 text-xs text-faint"><StatusIcon state={job.state} /><span className="truncate">{job.repository} · {friendlyName(currentRun(job)?.command || job.command)}</span><span className="ml-auto"><TaskTime job={job} /></span></span>
  </a>;
}

// Running tasks show how long they have been going; everything else shows when it last changed.
function TaskTime({ job }) {
  const run = currentRun(job);
  if (job.state === "running" && run?.started_at) return <time dateTime={run.started_at} title={`Started ${new Date(run.started_at).toLocaleString()}`}>{relativeTime(run.started_at).replace(" ago", "")}</time>;
  return <time dateTime={job.updated_at || job.created_at}>{relativeTime(job.updated_at || job.created_at).replace(" ago", "")}</time>;
}

function selectionChoices(status) {
  const workflows = status.workflows || [];
  return workflows.length ? workflows.map((name) => ({ value: `workflow:${name}`, label: friendlyName(name) })) : (status.commands || []).map((name) => ({ value: `command:${name}`, label: friendlyName(name) }));
}
function pick(values, current, remembered) { return values.includes(current) ? current : values.includes(remembered) ? remembered : values[0] || ""; }
function isTyping(target) { return target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName)); }
function readSetting(key) { try { return localStorage.getItem(key); } catch { return null; } }
function writeSetting(key, value) { try { localStorage.setItem(key, value); } catch { /* storage can be unavailable */ } }

export const appRoot = createRoot(document.getElementById("root"));
appRoot.render(<App />);
