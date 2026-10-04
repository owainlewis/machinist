import React, { useEffect, useState } from "react";
import { DisclosureAction } from "./components/ui/disclosure-action.jsx";
import { Button } from "./components/ui/button.jsx";
import { jobDisplayTitle } from "./runs-board.js";
import { formatTimestamp, stateLabel } from "./task-display.jsx";

export function FactoryHistory() {
  const [jobs, setJobs] = useState(null),
    [error, setError] = useState(""),
    [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const response = await fetch("/api/v1/status", {
          headers: { Accept: "application/json" },
        });
        if (!response.ok)
          throw Error(`Cannot load history (${response.status})`);
        const body = await response.json();
        if (active) {
          setJobs(body.jobs || []);
          setError("");
        }
      } catch (e) {
        if (active) setError(e.message);
      }
    }
    load();
    return () => {
      active = false;
    };
  }, [attempt]);
  return (
    <div className="factory-page factory-history">
      <p>
        Previous batch runs. New work is managed through your project’s foreman
        and board.
      </p>
      {error && (
        <div className="factory-error" role="alert">
          {error}
          <Button variant="ghost" onClick={() => setAttempt(attempt + 1)}>
            Try again
          </Button>
        </div>
      )}
      {!jobs && !error && <p role="status">Loading history…</p>}
      {jobs?.length === 0 && (
        <div className="factory-empty">
          <h2>No previous batch runs</h2>
          <p>Your current factory tasks are on each project’s board.</p>
        </div>
      )}
      {jobs?.map((job) => (
        <details className="factory-history-item" key={job.id}>
          <summary>
            <strong>{jobDisplayTitle(job)}</strong>
            <span>{stateLabel(job.state)}</span>
            <small>{formatTimestamp(job.created_at)}</small>
            <DisclosureAction />
          </summary>
          <p className="factory-muted">
            {job.repository} · {job.workflow?.name || job.command}
          </p>
          <p>{job.prompt}</p>
          {(job.runs || []).map((run) => (
            <section key={run.id}>
              <strong>
                {run.command || "Run"} · {stateLabel(run.state)}
              </strong>
              {run.summary && <pre>{run.summary}</pre>}
              {run.error && <p role="alert">{run.error}</p>}
              <small>
                {run.worker_name || "Worker unassigned"} ·{" "}
                {formatTimestamp(run.completed_at || run.started_at)}
              </small>
            </section>
          ))}
        </details>
      ))}
    </div>
  );
}
