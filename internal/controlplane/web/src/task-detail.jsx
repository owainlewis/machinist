import React, { useState } from "react";
import { ExternalLink, GitPullRequest } from "lucide-react";
import { Tabs } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/dialog";
import { Markdown } from "@/components/ui/markdown";
import { QuietState } from "@/components/ui/page-heading";
import { Spinner, StatusIcon } from "@/components/ui/status-icon";
import { cn } from "@/lib/utils";
import { Artifacts, useTaskArtifacts } from "./artifacts.jsx";
import { taskPresentation } from "./task-presentation.js";
import { currentRun, jobDisplayTitle, pullRequestURL, taskReason } from "./runs-board.js";
import { formatDurationMillis, formatTokenUsage } from "./run-metrics.js";
import { State, friendlyName, formatTimestamp, relativeTime, shortId, stateLabel } from "./task-display.jsx";

export function TaskDetail({ csrfToken, job, workers = [], loaded, error, deleting, onDelete, onWorkflowAction }) {
  const artifacts = useTaskArtifacts(job, csrfToken);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const breadcrumb = <header className="top-bar"><a href="#/tasks" className="text-muted-foreground hover:text-foreground">Tasks</a><span className="text-faint">/</span><h1 className="top-bar-title font-mono text-xs text-muted-foreground">{job ? shortId(job.id) : ""}</h1></header>;
  if (!job)
    return <>{breadcrumb}<div className="pane-scroll">{!loaded ? <QuietState title="Loading task" loading role="status" /> : <QuietState title="Task not found" description="It may have been deleted." />}{error && <p role="alert" className="px-4 text-center text-danger">{error}</p>}</div></>;
  const terminal = ["succeeded", "failed", "cancelled"].includes(job.state);
  const latest = job.runs.at(-1);
  const run = currentRun(job);
  const lastCompleted = job.runs.findLast((item) => item.outcome === "complete");
  const { result, history, stages } = taskPresentation(job);
  const reason = taskReason(job, workers);
  const pr = pullRequestURL(job);
  return <>
    {breadcrumb}
    <div className="pane-scroll">
      <div className="mx-auto max-w-3xl space-y-6 px-4 py-6 sm:px-6">
        <header className="space-y-2">
          <h2 className="break-words text-lg font-semibold tracking-tight">{jobDisplayTitle(job)}</h2>
          <p className="flex items-center gap-2"><StatusIcon state={job.state} /><span>{stateLabel(job.state)}</span><span className={cn("min-w-0 truncate", reason.tone ? `tone-${reason.tone}` : "text-faint")}>· {reason.text}</span></p>
          {error && <p role="alert" className="text-danger">{error}</p>}
        </header>

        {stages.length > 1 && <ol className="flex flex-wrap items-center gap-x-3 gap-y-1" aria-label="Task progress">
          {stages.map((stage, index) => <li key={index} className={cn("flex items-center gap-1.5", stage.current ? "text-foreground" : "text-faint")} aria-current={stage.current ? "step" : undefined}>
            {index > 0 && <span className="mr-1.5 h-px w-4 bg-border-strong" aria-hidden="true" />}
            <StatusIcon state={stage.complete ? "done" : stage.current ? job.state : "queued"} title={stage.complete ? "Complete" : undefined} />
            {friendlyName(stage.name)}
          </li>)}
        </ol>}

        {job.workflow && <TaskActions key={`${job.id}:${latest?.id}:${job.state}`} job={job} result={result} onAction={onWorkflowAction} />}

        <dl className="props">
          <dt>Repository</dt><dd>{job.repository}</dd>
          <dt>{job.workflow ? "Workflow" : "Agent"}</dt><dd>{friendlyName(job.workflow?.name || job.command)}{run?.model && <span className="text-faint">· {run.model}</span>}</dd>
          {job.task?.source_url && <><dt>Source</dt><dd><a className="inline-flex min-w-0 items-center gap-1 text-primary hover:underline" href={job.task.source_url} target="_blank" rel="noreferrer"><span className="truncate">{job.task.source_url.replace(/^https:\/\/github\.com\//, "")}</span><ExternalLink className="size-3 flex-none" /></a></dd></>}
          {pr && <><dt>Pull request</dt><dd><a className="inline-flex items-center gap-1 text-primary hover:underline" href={pr} target="_blank" rel="noreferrer"><GitPullRequest className="size-3.5" />{pr.replace(/^https:\/\/github\.com\//, "")}</a></dd></>}
          <dt>Worker</dt><dd>{run?.worker_name || <span className="text-faint">Not assigned yet</span>}{job.state === "running" && run?.started_at && <span className="text-faint">· running for {relativeTime(run.started_at).replace(" ago", "")}</span>}</dd>
          <dt>Created</dt><dd><time dateTime={job.created_at} title={formatTimestamp(job.created_at)}>{relativeTime(job.created_at)}</time></dd>
          <dt>Usage</dt><dd className="tabular-nums">{usage(job)}</dd>
        </dl>

        <Tabs key={job.id} label="Task sections" items={[
          { id: "result", label: "Result", content: <section className="space-y-4" aria-label="Current result">
            <h3 className="font-medium">{resultTitle(job, result)}</h3>
            {job.state === "running" && <p className="flex items-center gap-2 text-muted-foreground"><Spinner label="Working" />The agent is working. Results appear here when it finishes.</p>}
            {job.state === "queued" && <p className="flex items-center gap-2 text-muted-foreground"><StatusIcon state="queued" />{reason.text}.</p>}
            {result?.summary && <Markdown>{result.summary}</Markdown>}
            {result?.error && result.error !== result.summary && <pre role="alert" className="log-block text-danger">{result.error}</pre>}
            {result && job.task && <Artifacts key={result.id} artifacts={artifacts} runID={result.id} csrfToken={csrfToken} />}
          </section> },
          ...(job.task && lastCompleted ? [{ id: "files", label: "Files", content: <Artifacts artifacts={artifacts} runID={lastCompleted.id} csrfToken={csrfToken} /> }] : []),
          ...(history.length ? [{ id: "history", label: "History", content: <ol className="space-y-5">
            {history.map((item) => <li key={item.id} className="space-y-2 border-l border-border-strong pl-4">
              <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-medium">{item.outcome === "changes_requested" ? "Changes requested" : friendlyName(item.command)}</h3><State value={item.outcome === "complete" ? "succeeded" : item.outcome || item.state} /></div>
              {item.summary && <Markdown>{item.summary}</Markdown>}
              {item.error && item.error !== item.summary && <p className="text-danger">{item.error}</p>}
              {job.task && <Artifacts artifacts={artifacts} runID={item.id} csrfToken={csrfToken} />}
              <ExecutionDetails run={item} />
            </li>)}
          </ol> }] : []),
          { id: "instructions", label: "Instructions", content: <pre className="whitespace-pre-wrap break-words font-sans leading-6">{job.task ? job.task.spec || "Use the linked source for requirements." : job.prompt}</pre> },
          { id: "details", label: "Details", content: <div className="space-y-6">
            {result?.revision && <section><h3 className="mb-2 font-medium">Requested changes</h3><Markdown>{result.revision.feedback}</Markdown></section>}
            {result && <ExecutionDetails run={result} />}
            <section className="border-t border-border pt-4">
              <dl className="mb-4 grid gap-3 sm:grid-cols-3">
                <RunMetric label="Task ID" value={job.id} mono />
                <RunMetric label="Created" value={formatTimestamp(job.created_at)} />
                <RunMetric label="Updated" value={formatTimestamp(job.updated_at)} />
              </dl>
              <Button variant="outline" disabled={!terminal || deleting} onClick={() => setConfirmDelete(true)}>{deleting ? <><Spinner />Deleting</> : "Delete task"}</Button>
              {!terminal && <p className="mt-2 text-xs text-faint">Finished tasks can be deleted.</p>}
            </section>
          </div> },
        ]} />
      </div>
    </div>
    <Modal open={confirmDelete} onOpenChange={setConfirmDelete} title="Delete this task?" description={`${shortId(job.id)} and all of its stored run data and files will be removed. This cannot be undone.`}
      footer={<><Button variant="ghost" onClick={() => setConfirmDelete(false)}>Keep task</Button><Button className="border-danger bg-danger text-white" onClick={() => { setConfirmDelete(false); onDelete(job); }}>Delete task</Button></>} />
  </>;
}

function usage(job) {
  const runs = job.runs.filter((item) => Number.isSafeInteger(item.duration_millis));
  const millis = runs.reduce((total, item) => total + item.duration_millis, 0);
  const reported = job.runs.filter((item) => item.token_usage !== undefined && item.token_usage !== null);
  const tokens = reported.reduce((total, item) => total + Number(item.token_usage), 0);
  const parts = [runs.length ? formatDurationMillis(millis) : "", reported.length ? `${formatTokenUsage(String(tokens))} tokens` : ""].filter(Boolean);
  return parts.length ? parts.join(" · ") : <span className="text-faint">Not reported yet</span>;
}

function ExecutionDetails({ run }) {
  return <section aria-label="execution details" className="text-xs text-muted-foreground">
    <dl className="mt-3 grid gap-3 sm:grid-cols-3">
      <RunMetric label="Started" value={formatTimestamp(run.started_at)} />
      <RunMetric label="Completed" value={formatTimestamp(run.completed_at)} />
      <RunMetric label="Exit code" value={run.exit_code === undefined ? "Unavailable" : String(run.exit_code)} />
      <RunMetric label="Run ID" value={run.id} mono />
      <RunMetric label="Executor" value={run.executor} />
      <RunMetric label="Worker" value={run.worker_name || "Unassigned"} />
      <RunMetric label="Duration" value={Number.isSafeInteger(run.duration_millis) ? formatDurationMillis(run.duration_millis) : "Not available"} />
      <RunMetric label="Model" value={run.model || "Executor default"} />
      <RunMetric label="Tokens" value={formatTokenUsage(run.token_usage) === "Unavailable" ? "Not reported" : formatTokenUsage(run.token_usage)} />
    </dl>
  </section>;
}

function RunMetric({ label, value, mono = false }) {
  return <div className="min-w-0"><dt className="text-xs text-faint">{label}</dt><dd className={cn("mt-0.5 truncate text-foreground", mono && "font-mono text-xs")} title={value}>{value}</dd></div>;
}

// TaskActions shows only what the current state allows, inside one callout.
function TaskActions({ job, result, onAction }) {
  const [stopped, setStopped] = useState(false);
  const [requesting, setRequesting] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [busy, setBusy] = useState("");
  const latest = job.runs.at(-1);
  const action = async (name) => {
    setBusy(name);
    try {
      await onAction(job, name, stopped, name === "request_changes" ? feedback : "");
    } finally {
      setBusy("");
    }
  };
  const recovering = ["interrupted", "cancelled"].includes(job.state);
  const retry = ["blocked", "failed", "interrupted", "cancelled"].includes(job.state);
  const cancellable = ["queued", "running", "awaiting_approval", "blocked", "interrupted"].includes(job.state);
  const attention = ["awaiting_approval", "blocked", "interrupted", "failed"].includes(job.state);
  if (!retry && !cancellable && job.state !== "awaiting_approval") return null;
  const label = (name, text) => busy === name ? <><Spinner />{text}</> : text;
  return <div className={cn(attention ? "callout" : "flex flex-wrap items-center gap-2", job.state === "failed" && "callout-danger")}>
    {job.state === "awaiting_approval" && <p>{result ? <>Review the result of <b>{friendlyName(result.command).toLowerCase()}</b> above, then approve to start <b>{friendlyName(latest?.command).toLowerCase()}</b>.</> : <>Approve to start <b>{friendlyName(latest?.command).toLowerCase()}</b>.</>}</p>}
    {job.state === "blocked" && <p>The agent stopped and needs your input. Update the issue or resolve the blocker, then retry this step.</p>}
    {job.state === "interrupted" && <p>The worker stopped responding. Check that its process has stopped before retrying. A retry inspects existing work before continuing.</p>}
    {job.state === "failed" && <p>This step failed. Read the result below, fix the cause, then retry.</p>}
    {recovering && <label className="flex items-start gap-2"><input type="checkbox" className="mt-0.5 accent-[var(--primary)]" checked={stopped} onChange={(event) => setStopped(event.target.checked)} />I have verified the previous worker process has stopped.</label>}
    {requesting && <div className="space-y-2">
      <label className="block"><span className="field-label">What needs to change?</span><textarea className="field-control min-h-24" value={feedback} onChange={(event) => setFeedback(event.target.value)} maxLength={4000} placeholder="Explain what to revise in the previous step’s result." /></label>
      <p className="text-xs text-faint">You will review the revised result before continuing.</p>
    </div>}
    <div className="flex flex-wrap items-center gap-2">
      {cancellable && <Button variant="danger" disabled={Boolean(busy)} onClick={() => action("cancel")}>{label("cancel", "Cancel task")}</Button>}
      <span className="flex-1" />
      {job.state === "awaiting_approval" && !requesting && latest?.reviewed_run_id && <Button variant="outline" disabled={Boolean(busy)} onClick={() => setRequesting(true)}>Request changes</Button>}
      {requesting && <><Button variant="ghost" disabled={Boolean(busy)} onClick={() => setRequesting(false)}>Keep reviewing</Button><Button disabled={Boolean(busy) || !feedback.trim()} onClick={() => action("request_changes")}>{label("request_changes", "Send feedback and revise")}</Button></>}
      {job.state === "awaiting_approval" && !requesting && <Button disabled={Boolean(busy)} onClick={() => action("approve")}>{label("approve", `Approve and start ${friendlyName(latest?.command).toLowerCase()}`)}</Button>}
      {retry && <Button disabled={Boolean(busy) || (recovering && !stopped)} onClick={() => action("retry")}>{label("retry", `Retry ${friendlyName(latest?.command).toLowerCase()}`)}</Button>}
    </div>
  </div>;
}

function resultTitle(job, result) {
  const command = friendlyName(job.runs.at(-1)?.command);
  switch (job.state) {
    case "awaiting_approval": return result ? `${friendlyName(result.command)} ready for review` : `Ready to start ${command.toLowerCase()}`;
    case "running": return `${command} in progress`;
    case "queued": return `${command} queued`;
    case "succeeded": return "Task complete";
    default: return `${command} · ${stateLabel(job.state).toLowerCase()}`;
  }
}
