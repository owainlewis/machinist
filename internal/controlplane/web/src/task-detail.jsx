import React, { useState } from "react";
import { ArrowLeft, ArrowRight, Check } from "lucide-react";
import { Tabs } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { Artifacts, useTaskArtifacts } from "./artifacts.jsx";
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
  const terminal = ["succeeded", "failed", "cancelled"].includes(job.state);
  const latest = job.runs.at(-1);
  const lastCompleted = job.runs.findLast((run) => run.outcome === "complete");
  const { result, history, stages } = taskPresentation(job);
  const reviewing = job.state === "awaiting_approval";
  return (
    <div className="mx-auto max-w-[1000px] space-y-7 p-4 sm:p-6 lg:p-8">
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
                {friendlyName(stage.name)}
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
            label: "Result",
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
                {result && job.task && (
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
          ...(job.task && lastCompleted
            ? [
                {
                  id: "files",
                  label: "Files",
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
                  label: "History",
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
                                : friendlyName(run.command)}
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
                          {job.task && (
                            <Artifacts
                              artifacts={artifacts}
                              runID={run.id}
                              csrfToken={csrfToken}
                            />
                          )}
                          <ExecutionDetails run={run} />
                        </li>
                      ))}
                    </ol>
                  ),
                },
              ]
            : []),
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
                  <Button
                    variant="outline"
                    disabled={!terminal || deleting}
                    onClick={() => onDelete(job)}
                  >
                    {deleting ? "Deleting…" : "Delete task"}
                  </Button>
                </section>
              </div>
            ),
          },
        ]}
      />
    </div>
  );
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
  const latest = job.runs.at(-1);
  const prURL = (result || latest)?.summary?.match(
    /https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/pull\/\d+/,
  )?.[0];
  const action = async (name) => {
    setBusy(true);
    try {
      await onAction(
        job,
        name,
        stopped,
        name === "request_changes" ? feedback : "",
      );
    } finally {
      setBusy(false);
    }
  };
  const retry = ["blocked", "failed", "interrupted", "cancelled"].includes(
    job.state,
  );
  return (
    <div className="space-y-3">
      {prURL && (
        <Button asChild variant="outline">
          <a href={prURL} target="_blank" rel="noreferrer">
            Open PR
          </a>
        </Button>
      )}
      {job.state === "awaiting_approval" && (
        <div className="space-y-3">
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={busy || requesting}
              onClick={() => action("approve")}
            >
              Approve and start {friendlyName(latest?.command).toLowerCase()}
            </Button>
            {latest?.reviewed_run_id && (
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => setRequesting(true)}
              >
                Request changes
              </Button>
            )}
          </div>
          {requesting && (
            <div className="space-y-3">
              <label className="block">
                <span className="field-label">What needs to change?</span>
                <textarea
                  className="field-control min-h-24"
                  value={feedback}
                  onChange={(e) => setFeedback(e.target.value)}
                  maxLength={4000}
                  placeholder="Explain what to revise in the previous stage’s result."
                />
              </label>
              <p className="text-xs text-muted-foreground">
                You’ll review the revised result before continuing.
              </p>
              <div className="flex flex-wrap gap-2">
                <Button
                  disabled={busy || !feedback.trim()}
                  onClick={() => action("request_changes")}
                >
                  {busy ? "Submitting…" : "Send feedback and revise"}
                </Button>
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => setRequesting(false)}
                >
                  Keep reviewing
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
      {job.state === "blocked" && (
        <p className="text-sm text-muted-foreground">
          Update the issue or resolve the blocker, then retry this step.
        </p>
      )}
      {["interrupted", "cancelled"].includes(job.state) && (
        <label className="flex items-start gap-2 text-sm">
          <input
            type="checkbox"
            checked={stopped}
            onChange={(event) => setStopped(event.target.checked)}
          />
          I have verified the previous worker process has stopped. Retrying will
          inspect existing work before continuing.
        </label>
      )}
      {["queued", "running", "awaiting_approval", "blocked", "interrupted"].includes(
        job.state,
      ) && (
        <Button
          variant="ghost"
          className="text-muted-foreground hover:text-danger"
          disabled={busy}
          onClick={() => action("cancel")}
        >
          Cancel task
        </Button>
      )}
      {retry && (
        <Button
          disabled={
            busy ||
            (["interrupted", "cancelled"].includes(job.state) && !stopped)
          }
          onClick={() => action("retry")}
        >
          Retry {friendlyName(latest?.command).toLowerCase()}
        </Button>
      )}
    </div>
  );
}

function resultTitle(job, result) {
  const command = friendlyName(job.runs.at(-1)?.command);
  switch (job.state) {
    case "awaiting_approval":
      return result
        ? `${friendlyName(result.command)} ready for review`
        : `Ready to start ${command.toLowerCase()}`;
    case "running":
      return `${command} in progress`;
    case "queued":
      return `${command} queued`;
    case "succeeded":
      return "Task complete";
    default:
      return `${command} · ${stateLabel(job.state)}`;
  }
}
