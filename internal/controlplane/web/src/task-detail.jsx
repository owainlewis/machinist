import React, { useCallback, useEffect, useState } from "react";
import Markdown from "react-markdown";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import { useRunEvents, ExecutionGraph, AgentReports, PrimaryLog } from "./native-activity.jsx";
import { formatCost, formatTokens, runDuration, ticketFigures } from "./run-metrics.js";
import { formatTimestamp, State } from "./task-display.jsx";
import { isActive, issueLink, prLink, ticketSource } from "./task-presentation.js";
import { AnswerBox } from "./work.jsx";

const pollMillis = 2000;
const entryLabels = { request: "Request", comment: "Comment", review: "Review", ci: "CI", report: "Report", message: "Machinist" };

export function RunActivity({ run }) {
  const data = useRunEvents({ id: run.id, state: run.status });
  const [selected, setSelected] = useState(null);
  return <div className="run-activity">
    {data.error && <p className="form-error" role="alert">{data.error}</p>}
    <ExecutionGraph activities={data.children} runState={run.status} loaded={data.loaded} onSelect={setSelected} />
    {data.children.length > 0 && <AgentReports activities={data.children} selectedID={selected} onSelect={setSelected} />}
    <PrimaryLog logs={data.logs} />
  </div>;
}

function RunRow({ run, ticket }) {
  const [open, setOpen] = useState(false);
  const name = run.kind === "reviewer" ? `Reviewer: ${run.reviewer}` : "Agent run";
  return <li className="run">
    <div className="run-row">
      <strong>{name}</strong>
      <span className="ticket-meta">{run.status === "queued" ? "Waiting for a runner" : `${run.status.replaceAll("_", " ")} on ${run.runner || "a runner"}`}</span>
      <span className="ticket-meta">{run.harness}</span>
      <span className="ticket-meta">{runDuration(run)}</span>
      <span className="ticket-meta">{run.tokens === undefined ? "" : `${formatTokens(run.tokens)} tokens`}</span>
      <span className="ticket-meta">{run.cost === undefined ? "" : formatCost(run.cost)}</span>
      {run.status !== "queued" && <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen(value => !value)}>{open ? "Hide activity" : "Show activity"}<span className="sr-only"> for {name} on {ticket.title}</span></Button>}
    </div>
    {open && <RunActivity run={run} />}
  </li>;
}

export function TicketView({ detail, onChanged, onCancel, actionError }) {
  const { ticket, entries, runs, figures } = detail;
  const issue = issueLink(ticket);
  const pr = prLink(ticket);
  return <div className="page ticket-page">
    <a className="back-link" href="#/work"><ArrowLeft className="size-3.5" aria-hidden="true" />Work</a>
    <header className="ticket-header">
      <h1>{ticket.title || ticket.id}</h1>
      <div className="ticket-facts">
        <State value={ticket.state} />
        <span>{ticket.repo}</span>
        {issue ? <a href={issue} target="_blank" rel="noreferrer">{ticketSource(ticket)}</a> : <span>{ticketSource(ticket)}</span>}
        {pr && <a href={pr} target="_blank" rel="noreferrer">PR #{ticket.pr_number}</a>}
        <span>From {ticket.created_by}</span>
      </div>
    </header>
    {actionError && <p className="form-error" role="alert">{actionError}</p>}
    {ticket.state === "needs_you" && <section className="ticket-waiting" aria-label="Waiting on you"><AnswerBox ticket={ticket} onAnswered={onChanged} /></section>}
    <section aria-label="Runs">
      <h2>Runs</h2>
      {runs.length ? <ul className="plain-list">{runs.map(run => <RunRow key={run.id} run={run} ticket={ticket} />)}</ul> : <p className="page-note">No run has started yet.</p>}
    </section>
    <section aria-label="Figures" className="ledger">
      <h2>Figures</h2>
      <dl>{ticketFigures(figures).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    </section>
    <section aria-label="Log">
      <h2>Log</h2>
      <ol className="ticket-log">{entries.map(entry => <li key={entry.id}>
        <div className="ticket-meta"><strong>{entryLabels[entry.kind] || entry.kind}</strong> by {entry.author} at <time dateTime={entry.created_at}>{formatTimestamp(entry.created_at)}</time></div>
        <div className="run-prose"><Markdown skipHtml>{entry.body}</Markdown></div>
      </li>)}</ol>
    </section>
    {isActive(ticket) && <footer className="ticket-footer"><Button variant="outline" size="sm" onClick={onCancel}>Cancel ticket</Button></footer>}
  </div>;
}

export function TaskDetail({ ticketID, onSignedOut, onChanged }) {
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState("");
  const refresh = useCallback(async () => {
    try {
      const next = await api(`/api/v1/tickets/${encodeURIComponent(ticketID)}`);
      setDetail(next);
      setError("");
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    }
  }, [ticketID, onSignedOut]);
  useEffect(() => {
    setDetail(null);
    setError("");
    setActionError("");
    let stopped = false;
    let timer;
    const load = async () => {
      await refresh();
      if (!stopped) timer = window.setTimeout(load, pollMillis);
    };
    load();
    return () => { stopped = true; window.clearTimeout(timer); };
  }, [refresh]);
  async function changed() {
    await refresh();
    await onChanged?.();
  }
  async function cancel() {
    if (!window.confirm("Cancel this ticket and stop any run in progress? Its pull request stays open.")) return;
    setActionError("");
    try {
      await api(`/api/v1/tickets/${encodeURIComponent(ticketID)}/cancel`, { method: "POST" });
      await changed();
    } catch (failure) {
      setActionError(failure.message);
    }
  }
  if (!detail) return <div className="page">
    <a className="back-link" href="#/work"><ArrowLeft className="size-3.5" aria-hidden="true" />Work</a>
    {error ? <p className="form-error" role="alert">{error}</p> : <p className="page-note" role="status">Loading ticket</p>}
  </div>;
  return <TicketView detail={detail} onChanged={changed} onCancel={cancel} actionError={actionError} />;
}
