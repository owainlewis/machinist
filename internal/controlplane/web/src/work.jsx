import React, { useState } from "react";
import { Button } from "@/components/ui/button";
import { api } from "./api.js";
import { ledgerFigures } from "./run-metrics.js";
import { State, relativeTime } from "./task-display.jsx";
import { groupTickets, ticketSource } from "./task-presentation.js";

export function AnswerBox({ ticket, onAnswered }) {
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  async function submit(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await api(`/api/v1/tickets/${encodeURIComponent(ticket.id)}/answer`, { method: "POST", body: { text } });
      setText("");
      await onAnswered?.();
    } catch (failure) {
      setError(failure.message);
    } finally {
      setPending(false);
    }
  }
  return <form className="answer" onSubmit={submit}>
    {ticket.question && <p className="answer-question">{ticket.question}</p>}
    <label className="sr-only" htmlFor={`answer-${ticket.id}`}>Your answer</label>
    <textarea id={`answer-${ticket.id}`} className="field-control" rows={2} placeholder="Reply to continue this ticket" value={text} onChange={event => setText(event.target.value)} />
    {error && <p className="form-error" role="alert">{error}</p>}
    <Button type="submit" size="sm" disabled={pending || !text.trim()}>{pending ? "Sending" : "Send reply"}</Button>
  </form>;
}

function TicketRow({ ticket, onChanged }) {
  return <li className="ticket">
    <div className="ticket-row">
      <State value={ticket.state} label={false} />
      <a className="ticket-title" href={`#/work/${encodeURIComponent(ticket.id)}`}>{ticket.title || ticket.id}</a>
      <span className="ticket-meta">{ticket.repo}</span>
      <span className="ticket-meta ticket-source">{ticket.pr_number ? `PR #${ticket.pr_number}` : ticketSource(ticket)}</span>
      <time className="ticket-meta" dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time>
    </div>
    {ticket.state === "needs_you" && <AnswerBox ticket={ticket} onAnswered={onChanged} />}
  </li>;
}

export function WorkPage({ tickets = [], ledger, loaded, error, onChanged }) {
  const groups = groupTickets(tickets);
  const figures = ledgerFigures(ledger);
  return <div className="page work-page">
    <h1>Work</h1>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading tickets</p>}
    {loaded && !groups.length && <p className="page-note">Nothing yet. <a href="#/assign">Assign some work</a>, or mention the bot on an issue.</p>}
    {groups.map(group => <section key={group.id} aria-label={group.title} className="ticket-group">
      <h2>{group.title}<span>{group.tickets.length}</span></h2>
      <ul>{group.tickets.map(ticket => <TicketRow key={ticket.id} ticket={ticket} onChanged={onChanged} />)}</ul>
    </section>)}
    {figures.length > 0 && <section className="ledger" aria-label="Average per merged PR">
      <h2>Average per merged PR, across {ledger.merged_prs}</h2>
      <dl>{figures.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    </section>}
  </div>;
}
