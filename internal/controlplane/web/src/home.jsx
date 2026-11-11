import React, { useState } from "react";
import { Button } from "@/components/ui/button";
import { api } from "./api.js";
import { AssignBox } from "./assign.jsx";
import { elapsed, jobRole, queueLine, runningJobs, stalled } from "./runners.jsx";
import { State, relativeTime } from "./task-display.jsx";
import { isActive, prLink } from "./task-presentation.js";

const maxFinished = 5;

function ticketHref(id) {
  return `#/work/${encodeURIComponent(id)}`;
}

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

function Section({ id, title, count, children }) {
  return <section aria-labelledby={id}>
    <h2 id={id}>{title} {count > 0 && <span>{count}</span>}</h2>
    {children}
  </section>;
}

export function HomePage({ repositories = [], tickets = [], runners = [], queue, loaded, error, onChanged }) {
  const needsYou = tickets.filter(ticket => ticket.state === "needs_you");
  const ready = tickets.filter(ticket => ticket.state === "pr_ready");
  const running = runningJobs(runners);
  const waiting = queueLine(queue);
  const finished = tickets.filter(ticket => !isActive(ticket)).slice(0, maxFinished);
  const quiet = loaded && !needsYou.length && !ready.length && !running.length && !waiting && !finished.length;

  return <div className="page home-page" data-quiet={quiet || undefined}>
    <h1><label htmlFor="assign-work">What needs doing?</label></h1>
    <AssignBox repositories={repositories} />
    {error && <p className="form-error" role="alert">{error}</p>}

    {needsYou.length > 0 && <Section id="home-needs-you" title="Needs you" count={needsYou.length}>
      <ul className="plain-list">{needsYou.map(ticket => <li key={ticket.id} className="needs-you">
        <div className="queue-row">
          <a href={ticketHref(ticket.id)}>{ticket.title || ticket.id}</a>
          <span className="ticket-meta">{ticket.repo}</span>
          <time className="ticket-meta" dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time>
        </div>
        {ticket.question && <p className="question">{ticket.question}</p>}
        <Reply ticket={ticket} onChanged={onChanged} />
      </li>)}</ul>
    </Section>}

    {ready.length > 0 && <Section id="home-ready" title="Ready for review" count={ready.length}>
      <ul className="plain-list">{ready.map(ticket => <li key={ticket.id} className="queue-row">
        <a href={ticketHref(ticket.id)}>{ticket.title || ticket.id}</a>
        <span className="ticket-meta">{ticket.repo}</span>
        {ticket.pr_number > 0 && <a className="pr-link" href={prLink(ticket)} target="_blank" rel="noreferrer">PR #{ticket.pr_number}</a>}
        <time className="ticket-meta" dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time>
      </li>)}</ul>
    </Section>}

    {(running.length > 0 || waiting) && <Section id="home-running" title="Running now" count={running.length}>
      {waiting && <p className="page-note queue-note">{waiting}</p>}
      {stalled(runners, queue) && <p className="page-warning" role="status">No runner is online, so waiting runs cannot start. See <a href="#/runners">Runners</a>.</p>}
      {running.length > 0 && <ul className="plain-list">{running.map(job => <li key={job.run_id} className="queue-row">
        <a href={ticketHref(job.ticket_id)}>{job.title || job.ticket_id}</a>
        <span className="ticket-meta">{jobRole(job)}</span>
        <span className="ticket-meta">{job.runner}</span>
        <time className="ticket-meta" dateTime={job.started_at}>{elapsed(job.started_at)}</time>
      </li>)}</ul>}
    </Section>}

    {finished.length > 0 && <Section id="home-finished" title="Recently finished">
      <ul className="plain-list">{finished.map(ticket => <li key={ticket.id} className="queue-row">
        <a href={ticketHref(ticket.id)}>{ticket.title || ticket.id}</a>
        <State value={ticket.state} />
        <time className="ticket-meta" dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time>
      </li>)}</ul>
    </Section>}
  </div>;
}
