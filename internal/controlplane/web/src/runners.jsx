import React from "react";
import { formatDurationMillis } from "./run-metrics.js";
import { relativeTime } from "./task-display.jsx";

// A runner sends a heartbeat every 30 seconds while it is running work.
const onlineWindowMillis = 90_000;
const kinds = { local: "Local", kubernetes: "Kubernetes" };

export function runnerOnline(runner, now = Date.now()) {
  return now - Date.parse(runner.last_contact) < onlineWindowMillis;
}

// Every run in progress, longest running first, with the runner that holds it.
export function runningJobs(runners) {
  return runners
    .flatMap(runner => (runner.jobs || []).map(job => ({ ...job, runner: runner.name })))
    .sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at));
}

export function elapsed(since, now = Date.now()) {
  return formatDurationMillis(Math.max(0, now - Date.parse(since)));
}

export function jobRole(job) {
  if (job.kind !== "reviewer") return "Agent";
  return job.reviewer ? `Reviewer: ${job.reviewer}` : "Reviewer";
}

// Runs wait when no runner has a free slot for them.
export function queueLine(queue, now = Date.now()) {
  if (!queue?.waiting) return "";
  const wait = queue.oldest_queued_at ? elapsed(queue.oldest_queued_at, now) : "";
  if (queue.waiting === 1) return wait ? `1 run has waited ${wait} for a runner` : "1 run is waiting for a runner";
  return `${queue.waiting} runs are waiting for a runner${wait ? `, the oldest for ${wait}` : ""}`;
}

// Waiting runs cannot start while no runner is making contact.
export function stalled(runners, queue, now = Date.now()) {
  return Boolean(queue?.waiting) && !runners.some(runner => runnerOnline(runner, now));
}

export function slotWord(count) {
  return count === 1 ? "slot" : "slots";
}

// What the runners in contact can hold, and what they hold now.
export function fleet(runners, now = Date.now()) {
  const online = runners.filter(runner => runnerOnline(runner, now));
  return {
    online: online.length,
    slots: online.reduce((total, runner) => total + (runner.slots || 1), 0),
    busy: online.reduce((total, runner) => total + (runner.jobs || []).length, 0),
  };
}

function fleetSummary(runners, now = Date.now()) {
  const { online, slots, busy } = fleet(runners, now);
  return `${online} of ${runners.length} online, ${busy} of ${slots} ${slotWord(slots)} busy`;
}

function Runner({ runner }) {
  const online = runnerOnline(runner);
  const jobs = runner.jobs || [];
  const slots = runner.slots || 1;
  return <li>
    <div className="runner-row">
      <span className="runner-name"><strong>{runner.name}</strong>{kinds[runner.kind] && <span className="ticket-meta">{kinds[runner.kind]}</span>}</span>
      <span className="ticket-meta">{(runner.harnesses || []).join(", ")}</span>
      <span className="ticket-meta">{jobs.length} of {slots} {slotWord(slots)} busy</span>
      <time className="ticket-meta" dateTime={runner.last_contact}>Last contact {relativeTime(runner.last_contact)}</time>
      <span className={online ? "pill pill-success" : "pill pill-warning"}>{online ? "Online" : "Not responding"}</span>
    </div>
    {jobs.length > 0 && <ul className="runner-jobs">{jobs.map(job => <li key={job.run_id}>
      <a href={`#/work/${encodeURIComponent(job.ticket_id)}`}>{job.title || job.ticket_id}</a>
      <span className="ticket-meta">{jobRole(job)}</span>
      <time className="ticket-meta" dateTime={job.started_at}>{elapsed(job.started_at)}</time>
    </li>)}</ul>}
  </li>;
}

export function RunnersPage({ runners = [], queue, loaded, error }) {
  const waiting = queueLine(queue);
  return <div className="page runners-page">
    <header className="page-head">
      <div>
        <h1>Runners</h1>
        {runners.length > 0 && <p className="page-sub">{fleetSummary(runners)}{waiting && `. ${waiting}`}</p>}
      </div>
    </header>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading runners</p>}
    {loaded && !runners.length && <p className="empty">No runner has connected yet. Run <code>machinist runner start</code> on a machine or cluster.</p>}
    {stalled(runners, queue) && <p className="page-warning" role="status">No runner is online, so waiting runs cannot start.</p>}
    {runners.length > 0 && <ul className="list">{runners.map(runner => <Runner key={runner.name} runner={runner} />)}</ul>}
  </div>;
}
