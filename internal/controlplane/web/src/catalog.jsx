import { useEffect, useState } from "react";
import { ErrorBanner, QuietState, TopBar } from "@/components/ui/page-heading";
import { StatusIcon } from "@/components/ui/status-icon";
import { taskHref } from "@/routes";
import { jobDisplayTitle } from "@/runs-board";
import { relativeTime } from "./task-display.jsx";

export function WorkersPage({ workers, jobs = [], loaded, error }) {
  const running = jobs.filter((job) => job.state === "running");
  return <>
    <TopBar title="Workers" />
    {error && <ErrorBanner>{error}</ErrorBanner>}
    <div className="pane-scroll">
      {!loaded && !error ? <QuietState title="Checking workers" description="Checking live worker status." loading role="status" />
        : loaded && (workers.length ? workers.map((worker) => {
          const work = running.filter((job) => job.runs.some((run) => run.state === "running" && run.worker_name === worker.name));
          return <article key={worker.instance_id} className="row">
            <StatusIcon state={worker.connected ? "online" : "offline"} title={worker.connected ? "Connected" : "Disconnected"} />
            <div className="row-main">
              <h2 className="row-title">{worker.name}</h2>
              <p className="row-reason">{worker.connected ? "Connected" : "Disconnected"} · {work.length ? <>Running {work.map((job, index) => <span key={job.id}>{index > 0 && ", "}<a className="text-muted-foreground hover:text-foreground hover:underline" href={taskHref(job.id)}>{jobDisplayTitle(job)}</a></span>)}</> : worker.connected ? "Idle" : "Not taking work"}</p>
            </div>
            <div className="row-meta">
              <span className="desktop-only flex gap-1">{worker.repositories?.length ? worker.repositories.map((repository) => <span key={repository} className="rounded border border-border-strong px-1.5 font-mono text-[0.6875rem]">{repository}</span>) : "No repositories"}</span>
              <time className="w-28 text-right" dateTime={worker.last_seen_at} title={new Date(worker.last_seen_at).toLocaleString()}>Last seen {relativeTime(worker.last_seen_at)}</time>
            </div>
          </article>;
        }) : <QuietState title="No workers registered." description="Start a worker to register this machine with the control plane." />)}
    </div>
  </>;
}

export function TemplateHelp() {
  return <div className="space-y-3 text-muted-foreground">
    <p>The task supplies the request. A prompt tells an agent or script what to do with it.</p>
    <dl className="grid gap-3 sm:grid-cols-2">{[
      ["{{task.spec}}", "The instructions entered when creating the task."],
      ["{{task.title}}", "The task’s title."],
      ["{{task.source_url}}", "The linked issue or other source, if supplied."],
      ["{{task.output_dir}}", "Shared task files. Write plan.md here and read it in the next step."],
    ].map(([field, description]) => <div key={field}><dt className="font-mono text-xs text-foreground">{field}</dt><dd className="mt-0.5">{description}</dd></div>)}</dl>
    <p>Only save deliverables in the shared folder. Use <code className="font-mono text-xs">MACHINIST_SCRATCH_DIR</code> for temporary scripts, clones and logs.</p>
  </div>;
}

export function useDefinitions() {
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
