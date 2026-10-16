import React, { useEffect, useState } from "react";
import { ArrowLeft, ArrowRight, Check } from "lucide-react";
import { Tabs } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { Artifacts, useTaskArtifacts } from "./artifacts.jsx";
import { stageLabel } from "./stage-progress.jsx";
import { taskPresentation } from "./task-presentation.js";
import { jobDisplayTitle } from "./runs-board.js";
import { formatDurationMillis, formatTokenUsage } from "./run-metrics.js";
import {
  State,
  friendlyName,
  stateLabel,
  formatTimestamp,
} from "./task-display.jsx";

export function TaskDetail({
  csrfToken,
  job,
  loaded,
  error,
  deleting,
  onDelete,
  onWorkflowAction,
}) {
  const artifacts = useTaskArtifacts(job, csrfToken);
  const githubState = ["github", "local"].includes(job?.state_backend);
  const factoryTask = githubState || Boolean(job?.workflow?.factory);
  if (!job)
    return (
      <div className="p-8">
        <a href="#/runs" className="text-sm underline">
          Back to tasks
        </a>
        <p className="mt-4">{!loaded ? "Loading task…" : "Task not found."}</p>
        {error && <p role="alert">{error}</p>}
      </div>
    );
  if (factoryTask) return <FactoryTaskDetail job={job} error={error} onWorkflowAction={onWorkflowAction} />;
  const terminal = ["succeeded", "failed", "cancelled"].includes(job.state);
  const latest = job.runs.at(-1);
  const lastCompleted = job.runs.findLast((run) => run.outcome === "complete");
  const { result, history, stages } = taskPresentation(job);
  const reviewing = job.state === "awaiting_approval";
  return (
    <div className="mx-auto max-w-[1200px] space-y-7 p-4 sm:p-6 lg:p-8">
      <header className="space-y-4">
        <Button asChild variant="ghost" size="sm" className="-ml-3">
          <a href="#/runs">
            <ArrowLeft className="size-4" />
            Back to tasks
          </a>
        </Button>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="min-w-0 break-words text-2xl font-semibold">
            {jobDisplayTitle(job)}
          </h1>
          <State value={job.state} />
        </div>
        <p className="text-sm text-muted-foreground">
          {job.repository} · {friendlyName(job.workflow?.name || job.command)}
        </p>
        {job.task?.source_url && (
          <a
            className="block text-sm text-primary underline"
            href={job.task.source_url}
            target="_blank"
            rel="noreferrer"
          >
            Source ↗
          </a>
        )}
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        )}
      </header>
      {stages.length > 1 && (
        <ol
          className="flex flex-wrap items-center gap-3 text-sm"
          aria-label="Task progress"
        >
          {stages.map((stage, index) => (
            <li
              key={index}
              className="flex items-center gap-3"
              aria-current={stage.current ? "step" : undefined}
            >
              {index > 0 && (
                <ArrowRight
                  className="size-4 text-muted-foreground"
                  aria-hidden="true"
                />
              )}
              <span
                className={cn(
                  "flex items-center gap-2 py-1",
                  stage.current
                    ? "font-medium text-foreground"
                    : "text-muted-foreground",
                )}
              >
                {stage.complete ? (
                  <Check
                    className="size-4 text-success"
                    aria-label="Complete"
                  />
                ) : (
                  <span className="text-xs">{index + 1}</span>
                )}
                {stageLabel(stage.name)}
                {stage.skipped && <span className="text-xs text-muted-foreground">Skipped</span>}
                {stage.current && (
                  <span className="text-xs text-muted-foreground">
                    {reviewing ? "Awaiting approval" : stateLabel(job.state)}
                  </span>
                )}
              </span>
            </li>
          ))}
        </ol>
      )}
      <Tabs
        key={job.id}
        label="Task sections"
        items={[
          {
            id: "result",
            label: "Activity",
            content: (
              <Card
                className="space-y-5 p-5 sm:p-6"
                aria-label="Current result"
              >
                <h2 className="text-lg font-semibold">
                  {resultTitle(job, result)}
                </h2>
                {result?.summary && (
                  <p className="line-clamp-3 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">
                    {result.summary}
                  </p>
                )}
                {result?.error && result.error !== result.summary && (
                  <p
                    role="alert"
                    className="whitespace-pre-wrap break-words text-sm text-danger"
                  >
                    {result.error}
                  </p>
                )}
                {factoryTask && result?.github_url && <a className="block text-sm text-primary underline" href={result.github_url} target="_blank" rel="noreferrer">Open stage on GitHub</a>}
                {job.workflow?.factory?.spec_url && <a className="block text-sm text-primary underline" href={job.workflow.factory.spec_url} target="_blank" rel="noreferrer">Open spec on GitHub</a>}
                {result && job.task && !factoryTask && (
                  <Artifacts
                    key={result.id}
                    artifacts={artifacts}
                    runID={result.id}
                    csrfToken={csrfToken}
                  />
                )}
                {job.workflow && (
                  <TaskActions
                    key={`${job.id}:${latest?.id}:${job.state}`}
                    job={job}
                    result={result}
                    onAction={onWorkflowAction}
                  />
                )}
              </Card>
            ),
          },
          ...(job.task && lastCompleted && !factoryTask
            ? [
                {
                  id: "files",
                  label: "Artifacts",
                  content: (
                    <Artifacts
                      artifacts={artifacts}
                      runID={lastCompleted.id}
                      csrfToken={csrfToken}
                    />
                  ),
                },
              ]
            : []),
          ...(history.length
            ? [
                {
                  id: "history",
                  label: "Timeline",
                  content: (
                    <ol className="space-y-4">
                      {history.map((run) => (
                        <li
                          key={run.id}
                          className="space-y-3 border-l-2 border-border pl-4"
                        >
                          <div className="flex flex-wrap items-center justify-between gap-2">
                            <h3 className="font-medium">
                              {run.outcome === "changes_requested"
                                ? "Changes requested"
                                : stageLabel(run.command)}
                            </h3>
                            <State
                              value={
                                run.outcome === "complete"
                                  ? "succeeded"
                                  : run.outcome || run.state
                              }
                            />
                          </div>
                          {run.summary && (
                            <p className="whitespace-pre-wrap leading-6">
                              {run.summary}
                            </p>
                          )}
                          {run.error && run.error !== run.summary && (
                            <p className="text-danger">{run.error}</p>
                          )}
                          {job.task && !factoryTask && (
                            <Artifacts
                              artifacts={artifacts}
                              runID={run.id}
                              csrfToken={csrfToken}
                            />
                          )}
                          {run.github_url && <a className="block text-sm text-primary underline" href={run.github_url} target="_blank" rel="noreferrer">Open stage on GitHub</a>}
                          <ExecutionDetails run={run} />
                        </li>
                      ))}
                    </ol>
                  ),
                },
              ]
            : []),
          {
            id: "agents",
            label: "Agents",
            content: <div className="divide-y divide-border">{job.runs.map(run => <article key={run.id} className="py-4"><div className="flex flex-wrap justify-between gap-3"><h2 className="text-sm font-medium">{stageLabel(run.command)}</h2><State value={run.state} /></div><ExecutionDetails run={run} /><section className="mt-3 text-xs"><h3 className="text-muted-foreground">Prompt snapshot</h3><pre className="mt-3 overflow-auto whitespace-pre-wrap break-words font-mono leading-6">{run.prompt || "Prompt snapshot is not reported for this attempt. View the current agent definition in Agents."}</pre></section></article>)}</div>,
          },
          {
            id: "instructions",
            label: "Instructions",
            content: (
              <pre className="whitespace-pre-wrap break-words font-sans leading-6">
                {job.task
                  ? job.task.spec || "Use the linked source for requirements."
                  : job.prompt}
              </pre>
            ),
          },
          {
            id: "details",
            label: "Details",
            content: (
              <div className="space-y-6 text-sm">
                {result?.revision && (
                  <section>
                    <h2 className="mb-2 font-medium">Requested changes</h2>
                    <p className="whitespace-pre-wrap">
                      {result.revision.feedback}
                    </p>
                  </section>
                )}
                {result?.summary && (
                  <section>
                    <h2 className="mb-2 font-medium">Full summary</h2>
                    <p className="whitespace-pre-wrap leading-6">
                      {result.summary}
                    </p>
                  </section>
                )}
                {result && <ExecutionDetails run={result} />}
                <section className="border-t border-border pt-4">
                  <dl className="my-4 grid gap-3 sm:grid-cols-3">
                    <RunMetric label="Task ID" value={job.id} />
                    <RunMetric label="Repository" value={job.repository} />
                    <RunMetric
                      label="Created"
                      value={formatTimestamp(job.created_at)}
                    />
                    <RunMetric
                      label="Updated"
                      value={formatTimestamp(job.updated_at)}
                    />
                  </dl>
                  {githubState ? job.task?.source_url && <Button asChild variant="outline"><a href={job.task?.source_url} target="_blank" rel="noreferrer">Open issue</a></Button> : <Button
                    variant="outline"
                    disabled={!terminal || deleting}
                    onClick={() => onDelete(job)}
                  >
                    {deleting ? "Deleting…" : "Delete task"}
                  </Button>}
                </section>
              </div>
            ),
          },
        ]}
      />
    </div>
  );
}

function FactoryTaskDetail({ job, error, onWorkflowAction }) {
  const { result, history, stages } = taskPresentation(job);
  const latest = job.runs.at(-1);
  const progress = job.workflow?.factory;
  return (
    <article className="factory-task">
      <header className="factory-task-header">
        <a href="#/runs" className="factory-back"><ArrowLeft className="size-4" aria-hidden="true" />All runs</a>
        <div className="factory-task-title"><h1>{jobDisplayTitle(job)}</h1><State value={job.state} /></div>
        <div className="factory-task-meta"><span>{job.repository}</span>{job.task?.source_url && <><span>·</span><a href={job.task.source_url} target="_blank" rel="noreferrer">Open issue</a></>}</div>
      </header>
      {error && <p role="alert" className="text-sm text-danger">{error}</p>}
      <ol className="factory-progress" aria-label="Task progress">
        {stages.map(stage => <li key={stage.name} aria-current={stage.current ? "step" : undefined} className={cn(stage.current && "is-current", stage.complete && "is-complete")}>
          {stage.complete && <Check className="size-3.5" aria-label="Complete" />}{job.recipe === "single" ? "Agent" : job.recipe === "answer" ? "Answer" : stageLabel(stage.name)}{stage.skipped && <span>Skipped</span>}
        </li>)}
      </ol>
      <Tabs key={job.id} label="Run sections" items={[
        {id:"activity",label:"Activity",content:<>      <section className="factory-result" aria-label="Current result">
        <h2>{resultTitle(job, result)}</h2>
        {result?.summary && <p className="factory-summary">{job.state === "failed" && /^invalid character .*looking for beginning of value/.test(result.summary) ? "The agent returned an unreadable result. Retry this stage, or request changes on the existing PR." : result.summary}</p>}
        {result?.error && result.error !== result.summary && <p role="alert" className="factory-summary text-danger">{result.error}</p>}
        {job.state === "running" ? <RunActivity run={latest} /> : !result?.summary && <p className="factory-summary">{job.state === "queued" ? "Waiting for a runner to pick up this task." : "No result was reported."}</p>}
        {progress?.spec_url && <a className="factory-spec-link" href={progress.spec_url} target="_blank" rel="noreferrer">Open spec on GitHub<ArrowRight className="size-4" aria-hidden="true" /></a>}
        <TaskActions key={`${job.id}:${latest?.id}:${job.state}:${job.hold_version || 0}`} job={job} result={result} onAction={onWorkflowAction} />
      </section>
      {history.length > 0 && <section className="factory-history" aria-label="Previous attempts"><h2>Earlier activity</h2><ol>{history.map(run => <li key={run.id}><div className="flex flex-wrap items-center justify-between gap-2"><span className="font-medium">{stageLabel(run.command)}</span><State value={run.outcome === "complete" ? "succeeded" : run.outcome || run.state} /></div><p>{run.summary || run.error}</p>{run.github_url && <a href={run.github_url} target="_blank" rel="noreferrer">View on GitHub</a>}</li>)}</ol></section>}
</>},
        {id:"details",label:"Details",content:<>      <section className="factory-details"><h2 className="text-sm font-medium">Run details</h2>
        {result && <ExecutionDetails run={result} />}
        <dl className="mt-5 grid gap-3 sm:grid-cols-2"><RunMetric label="Task ID" value={job.id} /><RunMetric label="Updated" value={formatTimestamp(job.updated_at)} /></dl>
        {result?.github_url && <a className="mt-5 block text-sm text-primary underline" href={result.github_url} target="_blank" rel="noreferrer">Open stage on GitHub</a>}
        <h2 className="mt-6 mb-2 text-sm font-medium">Original request</h2><p className="whitespace-pre-wrap break-words text-sm leading-6">{job.task?.spec || job.prompt}</p>
      </section></>},
      ]} />

    </article>
  );
}

export function RunActivity({ run, now }) {
  const [clock, setClock] = useState(Date.now);
  useEffect(() => {
    if (now !== undefined) return;
    const timer = setInterval(() => setClock(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [now]);
  now ??= clock;
  const phases = { preparing: "Preparing the workspace", checking_pr: "Checking the pull request and CI", starting_sandbox: "Starting the Kubernetes sandbox", running_agent: "Agent running", collecting_result: "Collecting the result and stopping the sandbox", publishing: "Publishing the branch and pull request" };
  const age = run?.heartbeat_at ? Math.max(0, Math.floor((now - Date.parse(run.heartbeat_at)) / 1000)) : null;
  const elapsed = run?.started_at ? Math.max(0, now - Date.parse(run.started_at)) : null;
  return <div className="space-y-2">
    <p role="status" className="factory-summary">{phases[run?.phase] || (age === null ? "Waiting for runner status" : "Runner active")}</p>
    <p className={cn("text-xs", age !== null && age > 20 ? "text-warning" : "text-muted-foreground")}>
      {run?.worker_name && <span>{run.worker_name} · </span>}
      {age === null ? "No heartbeat received yet." : age > 20 ? `Runner status delayed. Last heartbeat ${age}s ago. The run may be interrupted.` : `Runner connected · heartbeat ${age}s ago.`}
      {elapsed !== null && <span> · Elapsed {formatDurationMillis(Math.floor(elapsed / 1000) * 1000)}</span>}
    </p>
  </div>;
}

function ExecutionDetails({ run }) {
  return (
    <section
      aria-label="execution details"
      className="text-xs text-muted-foreground"
    >
      <dl className="mt-3 grid gap-3 sm:grid-cols-3">
        <RunMetric label="Started" value={formatTimestamp(run.started_at)} />
        <RunMetric
          label="Completed"
          value={formatTimestamp(run.completed_at)}
        />
        <RunMetric
          label="Exit code"
          value={
            run.exit_code === undefined ? "Unavailable" : String(run.exit_code)
          }
        />
        <RunMetric label="Run ID" value={run.id} mono />
        <RunMetric label="Executor" value={run.executor} />
        <RunMetric label="Worker" value={run.worker_name || "Unassigned"} />
        <RunMetric
          label="Duration"
          value={
            Number.isSafeInteger(run.duration_millis)
              ? formatDurationMillis(run.duration_millis)
              : "Not available"
          }
        />
        <RunMetric label="Model" value={run.model || "Executor default"} />
        <RunMetric
          label="Tokens"
          value={
            formatTokenUsage(run.token_usage) === "Unavailable"
              ? "Not reported"
              : formatTokenUsage(run.token_usage)
          }
        />
      </dl>
    </section>
  );
}

function RunMetric({ label, value, mono = false }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd
        className={cn("mt-0.5 truncate text-sm", mono && "font-mono")}
        title={value}
      >
        {value}
      </dd>
    </div>
  );
}

function TaskActions({ job, result, onAction }) {
  const [stopped, setStopped] = useState(false);
  const [requesting, setRequesting] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [busy, setBusy] = useState(false);
  const [releaseConfirmed, setReleaseConfirmed] = useState(false);
  const latest = job.runs.at(-1);
  const prURL = job.workflow?.factory?.pr_url || (result || latest)?.summary?.match(/https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/pull\/\d+/)?.[0];
  const needsStopped = ["interrupted", "cancelled"].includes(job.state);
  const retryable = ["blocked", "failed", "interrupted", "cancelled"].includes(job.state);
  const humanDecision = job.state === "blocked" && job.workflow?.factory?.verdict === "human_decision_needed";
  const canRequest = job.recipe !== "answer" && (job.state === "awaiting_merge" || humanDecision || (job.state === "awaiting_approval" && latest?.reviewed_run_id) || (["failed", "blocked"].includes(job.state) && latest?.command === "review" && prURL));
  const stage = job.recipe === "single" ? "agent" : job.recipe === "answer" ? "answer" : stageLabel(latest?.command).toLowerCase();
  const act = async name => {
    setBusy(true);
    try { await onAction(job, name, stopped, ["request_changes", "retry"].includes(name) ? feedback : name === "publication_hold" ? "Operator requested private publication hold" : ""); }
    finally { setBusy(false); }
  };
  const dismissAttention = (["github", "local"].includes(job.state_backend) || Boolean(job.workflow?.factory)) && !job.inbox_cleared && ["blocked", "failed", "interrupted", "timed_out"].includes(job.state) && <Button variant="ghost" disabled={busy} onClick={() => act("clear_inbox")}>Dismiss from attention</Button>;
  const stopConfirmation = needsStopped && <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={stopped} onChange={event => setStopped(event.target.checked)} />I have verified the previous worker process has stopped.</label>;
  if (job.publication_hold) return <section className="publication-hold"><h3>Publication held private</h3><p>{job.publication_hold}</p><p>Review the content and destination before releasing this hold. Retry and prompt changes cannot release it.</p><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={releaseConfirmed} onChange={event => setReleaseConfirmed(event.target.checked)} />I allow publication to {job.repository}.</label>{stopConfirmation}<Button className="mt-4" variant="outline" disabled={busy || !releaseConfirmed || (needsStopped && !stopped)} onClick={() => act("release_publication")}>Release publication hold</Button>{dismissAttention}</section>;
  return <div className="task-actions">
    {humanDecision ? <p className="action-hint">The reviewer needs your judgment. Send a decision to Build, or retry Review after resolving the question.</p> : job.state === "blocked" ? <p className="action-hint">Resolve the blocker or reply to the agent before retrying.</p> : needsStopped ? <p className="action-hint">Confirm the previous process has stopped before continuing.</p> : job.state === "awaiting_merge" ? <p className="action-hint">{job.recipe === "single" ? "Single-agent mode has no independent review. Inspect the PR and CI on GitHub before merging, then check its status here." : "Review and CI must match the current PR head. Merge on GitHub when ready, then check its status here."}</p> : null}
    {(retryable || requesting) && <label className="block"><span className="field-label">{requesting ? "What needs to change?" : "Reply to the agent (optional)"}</span><textarea className="field-control min-h-24" value={feedback} onChange={event => setFeedback(event.target.value)} maxLength={4000} placeholder={requesting ? "Explain the change to make on the existing PR." : "Answer the question or explain what changed."} /></label>}
    {stopConfirmation}
    <div className="task-action-row">
      {requesting ? <><Button disabled={busy || !feedback.trim()} onClick={() => act("request_changes")}>{busy ? "Sending…" : "Send feedback and revise"}</Button><Button variant="outline" disabled={busy} onClick={() => setRequesting(false)}>Keep reviewing</Button></> : <>
        {job.state === "awaiting_approval" && <Button disabled={busy} onClick={() => act("approve")}>Approve and start {stage}</Button>}
        {job.state === "awaiting_merge" && <Button disabled={busy} onClick={() => act("complete_merge")}>Check merge status</Button>}
        {humanDecision && <Button disabled={busy} onClick={() => setRequesting(true)}>Request changes</Button>}
        {retryable && <Button variant={humanDecision ? "outline" : "default"} disabled={busy || (needsStopped && !stopped)} onClick={() => act("retry")}>Retry {stage}</Button>}
        {canRequest && !humanDecision && <Button variant="outline" disabled={busy} onClick={() => setRequesting(true)}>Request changes</Button>}
      </>}
      {prURL && <Button asChild variant="outline"><a href={prURL} target="_blank" rel="noreferrer">Open PR</a></Button>}
      <div className="task-secondary-actions">
        {dismissAttention}
        {(retryable || ["awaiting_approval", "awaiting_merge"].includes(job.state)) && <Button variant="ghost" disabled={busy || (needsStopped && !stopped)} onClick={() => act("publication_hold")}>Keep private</Button>}
        {["queued", "running", "awaiting_approval", "awaiting_merge", "blocked", "interrupted"].includes(job.state) && <Button variant="ghost" disabled={busy} onClick={() => act("cancel")}>Cancel task</Button>}
      </div>
    </div>
  </div>;
}

function resultTitle(job, result) {
  const command = job.recipe === "single" ? "Agent" : job.recipe === "answer" ? "Answer" : stageLabel(job.runs.at(-1)?.command);
  switch (job.state) {
    case "awaiting_approval":
      return result
        ? `${stageLabel(result.command)} ready for review`
        : `Ready to start ${command.toLowerCase()}`;
    case "running":
      return `${command} in progress`;
    case "queued":
      return `${command} queued`;
    case "awaiting_merge":
      return "Pull request ready for your review";
    case "succeeded":
      return "Task complete";
    default:
      return `${command} · ${stateLabel(job.state)}`;
  }
}
