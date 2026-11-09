import React from "react";
import { relativeTime } from "./task-display.jsx";

// A runner sends a heartbeat every 30 seconds while it is running work.
const onlineWindowMillis = 90_000;

export function runnerOnline(runner, now = Date.now()) {
  return now - Date.parse(runner.last_contact) < onlineWindowMillis;
}

export function RunnersPage({ runners = [], loaded, error }) {
  return <div className="page runners-page">
    <h1>Runners</h1>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading runners</p>}
    {loaded && !runners.length && <p className="page-note">No runner has connected since Machinist started. Run <code>machinist runner start</code> on a machine or cluster.</p>}
    {runners.length > 0 && <ul className="plain-list">{runners.map(runner => <li key={runner.name} className="runner-row">
      <span className={runnerOnline(runner) ? "runner-online" : "runner-offline"}>{runnerOnline(runner) ? "Online" : "Not responding"}</span>
      <strong>{runner.name}</strong>
      <span className="ticket-meta">{(runner.harnesses || []).join(", ") || "Busy"}</span>
      <time className="ticket-meta" dateTime={runner.last_contact}>Last contact {relativeTime(runner.last_contact)}</time>
    </li>)}</ul>}
  </div>;
}
