import React, { useState } from "react";
import { Trash2 } from "lucide-react";
import { api, SignedOutError } from "./api.js";
import { AgentBadges } from "./agent-badge.jsx";
import { harnessLabel, harnessList } from "./agents.js";
import { HarnessMark } from "./harness-mark.jsx";
import { relativeTime } from "./display.jsx";
import { formatDuration } from "./jobs.js";
import { jobHref } from "./routes.js";
import { Button } from "@/components/ui/button";

// A runner sends a heartbeat every 30 seconds while it is running work.
const onlineWindowMillis = 90_000;
const kinds = { local: "Local", kubernetes: "Kubernetes" };

// runnerHarnesses is the agent harnesses a runner reported, without the steps
// every runner runs.
export function runnerHarnesses(runner) {
  return (runner.harnesses || []).filter(name => name !== "steps");
}

export function runnerOnline(runner, now = Date.now()) {
  return now - Date.parse(runner.last_contact) < onlineWindowMillis;
}

export function elapsed(since, now = Date.now()) {
  return formatDuration(Math.max(0, now - Date.parse(since)));
}

// Runs wait when no runner has a free slot for them.
export function queueLine(queue, now = Date.now()) {
  if (!queue?.waiting) return "";
  const wait = queue.oldest_queued_at ? elapsed(queue.oldest_queued_at, now) : "";
  if (queue.waiting === 1) return wait ? `1 run has waited ${wait} for a runner` : "1 run is waiting for a runner";
  return `${queue.waiting} runs are waiting for a runner${wait ? `, the oldest for ${wait}` : ""}`;
}

// harnessWaits says which queued runs no online runner can take, because none
// has the harnesses their agents run.
export function harnessWaits(queue) {
  return (queue?.blocked || []).map(wait => `${wait.waiting === 1 ? "1 run is" : `${wait.waiting} runs are`} waiting for a runner with ${harnessList(wait.harnesses)}`);
}

// A runner can be removed while it is out of contact and holds no job. One
// that asks for work again comes back.
export function removable(runner, now = Date.now()) {
  return !runnerOnline(runner, now) && !(runner.jobs || []).length;
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

// SlotMeter draws one segment per slot, lit while a job holds it. A large
// runner gets a single bar instead, so the row keeps its width.
const maxSegments = 12;

export function SlotMeter({ busy, slots }) {
  const label = `${busy} of ${slots} ${slotWord(slots)} busy`;
  if (slots > maxSegments) return <span className="slot-meter slot-meter-bar" role="img" aria-label={label}><span style={{ width: `${Math.min(1, busy / slots) * 100}%` }} /></span>;
  return <span className="slot-meter" role="img" aria-label={label}>{Array.from({ length: slots }, (_, index) => <span key={index} data-busy={index < busy || undefined} />)}</span>;
}

// RemoveRunner asks before it forgets a runner, since the fleet shows it
// again only once it asks for work.
function RemoveRunner({ runner, onRemoved, onSignedOut }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function remove() {
    setBusy(true);
    try {
      await api(`/api/v1/runners/${encodeURIComponent(runner.name)}`, { method: "DELETE" });
      setError("");
      await onRemoved?.();
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }
  return <div className="runner-actions">
    {error && <span className="form-error" role="alert">{error}</span>}
    {!confirming && <Button variant="ghost" size="sm" onClick={() => setConfirming(true)} disabled={busy} aria-label={`Remove ${runner.name}`}><Trash2 className="size-3.5" aria-hidden="true" />Remove</Button>}
    {confirming && <span className="token-confirm" role="group" aria-label={`Remove ${runner.name}?`}>
      <span className="ticket-meta">It comes back if it asks for work again.</span>
      <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={busy}>Keep</Button>
      <Button variant="outline" size="sm" className="text-danger" onClick={remove} disabled={busy}>Remove</Button>
    </span>}
  </div>;
}

function Runner({ runner, onChange, onSignedOut }) {
  const online = runnerOnline(runner);
  const jobs = runner.jobs || [];
  const slots = runner.slots || 1;
  return <li>
    <div className="runner-row">
      <span className="runner-name"><strong>{runner.name}</strong>{kinds[runner.kind] && <span className="ticket-meta">{kinds[runner.kind]}</span>}{runnerHarnesses(runner).length > 0 && <span className="runner-harnesses">{runnerHarnesses(runner).map(name => <span key={name} className="runner-harness"><HarnessMark harness={name} /><span>{harnessLabel(name)}</span></span>)}</span>}</span>
      <span className="runner-slots"><SlotMeter busy={jobs.length} slots={slots} /><span className="ticket-meta">{jobs.length} of {slots} {slotWord(slots)} busy</span></span>
      <time className="ticket-meta" dateTime={runner.last_contact}>Last contact {relativeTime(runner.last_contact)}</time>
      <span className={online ? "pill pill-success" : "pill pill-warning"}><span className="live-dot" data-live={online || undefined} aria-hidden="true" />{online ? "Online" : "Not responding"}</span>
    </div>
    {removable(runner) && <RemoveRunner runner={runner} onRemoved={onChange} onSignedOut={onSignedOut} />}
    {jobs.length > 0 && <ul className="runner-jobs">{jobs.map(job => <li key={job.run_id}>
      <a href={jobHref(job.ticket_id)}>{job.title || job.ticket_id}</a>
      {job.step && <span className="ticket-meta runner-job-step">{job.step}</span>}
      <AgentBadges agents={job.agents} />
      <time className="ticket-meta" dateTime={job.started_at}>{elapsed(job.started_at)}</time>
    </li>)}</ul>}
  </li>;
}

export function RunnersPage({ runners = [], queue, loaded, error, onChange, onSignedOut }) {
  const waiting = queueLine(queue);
  const waits = harnessWaits(queue);
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
    {waits.length > 0 && <div className="page-warning" role="status">
      <p>{waits.join(". ")}. No online runner has {waits.length === 1 ? "it" : "them"}.</p>
      <p>Install the harness on a runner and restart the runner, or change the presets' agents.</p>
    </div>}
    {runners.length > 0 && <ul className="list">{runners.map(runner => <Runner key={runner.name} runner={runner} onChange={onChange} onSignedOut={onSignedOut} />)}</ul>}
  </div>;
}
