import React from "react";
import { ledgerFigures } from "./run-metrics.js";
import { State, relativeTime } from "./task-display.jsx";
import { sortTickets } from "./task-presentation.js";

function JobRow({ ticket }) {
  return <tr>
    <td className="job-title"><a href={`#/work/${encodeURIComponent(ticket.id)}`}>{ticket.title || ticket.id}</a></td>
    <td><State value={ticket.state} /></td>
    <td className="job-detail">{ticket.repo}</td>
    <td className="job-detail">{ticket.pr_number ? `#${ticket.pr_number}` : ""}</td>
    <td className="job-updated"><time dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time></td>
  </tr>;
}

export function WorkPage({ tickets = [], ledger, loaded, error }) {
  const jobs = sortTickets(tickets);
  const figures = ledgerFigures(ledger);
  return <div className="page work-page">
    <h1>Work</h1>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading tickets</p>}
    {loaded && !jobs.length && <p className="page-note">Nothing yet. <a href="#/home">Assign some work</a>, or mention the bot on an issue.</p>}
    {jobs.length > 0 && <table className="jobs">
      <thead><tr><th scope="col">Job</th><th scope="col">Status</th><th scope="col" className="job-detail">Repository</th><th scope="col" className="job-detail">PR</th><th scope="col" className="job-updated">Updated</th></tr></thead>
      <tbody>{jobs.map(ticket => <JobRow key={ticket.id} ticket={ticket} />)}</tbody>
    </table>}
    {figures.length > 0 && <section className="ledger" aria-label="Average per merged PR">
      <h2>Average per merged PR, across {ledger.merged_prs}</h2>
      <dl>{figures.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    </section>}
  </div>;
}
