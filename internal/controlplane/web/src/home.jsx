import React, { useState } from "react";
import { Button } from "@/components/ui/button";
import { api } from "./api.js";
import { AssignBox } from "./assign.jsx";
import { DayChart } from "./charts.jsx";
import { formatCompact, formatCost, formatTokens } from "./run-metrics.js";
import { elapsed, fleet, jobRole, queueLine, runningJobs, slotWord, stalled } from "./runners.jsx";
import { State, relativeTime } from "./task-display.jsx";
import { isActive, prLink, sortTickets } from "./task-presentation.js";
import { usageTotals } from "./usage.js";

const maxFinished = 5;
const runSeries = [{ key: "succeeded", label: "Passed" }, { key: "failed", label: "Failed" }];
const tokenSeries = [{ key: "tokens", label: "Tokens" }];

// A short answer goes back from here. A longer one has the ticket page.
function Reply({ ticket, onChanged }) {
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  async function submit(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await api(`/api/v1/tickets/${encodeURIComponent(ticket.id)}/answer`, { method: "POST", body: { text: text.trim() } });
      setText("");
      await onChanged?.();
    } catch (failure) {
      setError(failure.message);
    } finally {
      setPending(false);
    }
  }

  return <form className="reply" onSubmit={submit}>
    <input className="field-control" value={text} onChange={event => setText(event.target.value)} placeholder="Reply" aria-label={`Reply to ${ticket.title || ticket.id}`} />
    <Button type="submit" variant="outline" disabled={pending || !text.trim()}>{pending ? "Sending" : "Reply"}</Button>
    {error && <p className="form-error" role="alert">{error}</p>}
  </form>;
}

function Tile({ label, value, unit, sub, note }) {
  return <div className="tile">
    <h2 className="label">{label}</h2>
    <p className="tile-value">{value}{unit && <span>{unit}</span>}</p>
    <p className="tile-sub">{sub}</p>
    {note && <p className="tile-note">{note}</p>}
  </div>;
}

// One row per ticket. Its status is on the row, and a question can be answered in place.
function WorkRow({ ticket, job, onChanged }) {
  const pr = prLink(ticket);
  return <li>
    <div className="row-line">
      <div className="row-title"><a href={`#/work/${encodeURIComponent(ticket.id)}`}>{ticket.title || ticket.id}</a><span className="ticket-meta">{ticket.repo}</span></div>
      <div className="row-detail">
        {job && <span>{jobRole(job)} on {job.runner}, <time dateTime={job.started_at}>{elapsed(job.started_at)}</time></span>}
        {pr && <a className="pr-link" href={pr} target="_blank" rel="noreferrer">PR #{ticket.pr_number}</a>}
      </div>
      <State value={ticket.state} />
      <time className="row-time" dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time>
    </div>
    {ticket.state === "needs_you" && <div className="row-extra">
      {ticket.question && <p className="question">{ticket.question}</p>}
      <Reply ticket={ticket} onChanged={onChanged} />
    </div>}
  </li>;
}

export function HomePage({ repositories = [], tickets = [], runners = [], queue, usage, loaded, error, onChanged }) {
  const sorted = sortTickets(tickets);
  const recent = [...sorted.filter(isActive), ...sorted.filter(ticket => !isActive(ticket)).slice(0, maxFinished)];
  const needsYou = tickets.filter(ticket => ticket.state === "needs_you").length;
  const ready = tickets.filter(ticket => ticket.state === "pr_ready").length;
  const running = runningJobs(runners);
  const { online, slots, busy } = fleet(runners);
  const days = usage?.days || [];
  const totals = usageTotals(days);
  const period = `Last ${days.length} days`;

  return <div className="page home-page">
    <header className="page-head"><h1><label htmlFor="assign-work">What needs doing?</label></h1></header>
    <AssignBox repositories={repositories} />
    {error && <p className="form-error" role="alert">{error}</p>}
    {stalled(runners, queue) && <p className="page-warning" role="status">No runner is online, so waiting runs cannot start. See <a href="#/runners">Runners</a>.</p>}

    <div className="tiles">
      <Tile label="Running now" value={busy} unit={`of ${slots} ${slotWord(slots)}`} sub={queueLine(queue) || "No runs waiting"} note={runners.length ? `${online} of ${runners.length} runners online` : "No runner connected"} />
      <Tile label="Needs you" value={needsYou} unit={needsYou === 1 ? "job" : "jobs"} sub={ready ? `${ready} ${ready === 1 ? "PR" : "PRs"} ready for review` : "No PRs waiting for review"} />
      {totals.judged
        ? <Tile label="Pass rate" value={`${totals.passRate}%`} sub={`${totals.succeeded} of ${totals.judged} runs passed`} note={period} />
        : <Tile label="Pass rate" value={0} unit="runs" sub="No run has finished" note={days.length > 0 && period} />}
      <Tile label="Tokens" value={formatCompact(totals.tokens)} sub={totals.cost > 0 ? `${formatCost(totals.cost)} reported cost` : "No cost reported"} note={days.length > 0 && period} />
    </div>

    <div className="charts">
      <DayChart title="Runs per day" days={days} series={runSeries} empty="No run has finished in this period" note={days.length > 0 && `${period}, by UTC day. Cancelled runs are left out.`} />
      <DayChart title="Tokens per day" days={days} series={tokenSeries} format={formatTokens} axisFormat={formatCompact} empty="No tokens used in this period" note={days.length > 0 && `${period}, by UTC day.`} />
    </div>

    <section className="recent" aria-labelledby="home-recent">
      <div className="section-head"><h2 id="home-recent">Recent work</h2>{recent.length > 0 && <a href="#/work">All work</a>}</div>
      {recent.length > 0 && <ul className="list">{recent.map(ticket => <WorkRow key={ticket.id} ticket={ticket} job={running.find(job => job.ticket_id === ticket.id)} onChanged={onChanged} />)}</ul>}
      {loaded && !recent.length && <p className="empty">No work yet. Describe a job in the box above and an agent picks it up.</p>}
    </section>
  </div>;
}
