import React, { useState } from "react";
import { ledgerFigures } from "./run-metrics.js";
import { State, relativeTime } from "./task-display.jsx";
import { isActive, sortTickets, stateLabels } from "./task-presentation.js";

const filters = [
  ["all", "All", () => true],
  ...["needs_you", "working", "pr_ready"].map(state => [state, stateLabels[state], ticket => ticket.state === state]),
  ["finished", "Finished", ticket => !isActive(ticket)],
];

function JobRow({ ticket }) {
  return <tr>
    <td className="job-title"><a href={`#/work/${encodeURIComponent(ticket.id)}`}>{ticket.title || ticket.id}</a></td>
    <td className="job-detail">{ticket.repo}</td>
    <td className="job-detail">{ticket.pr_number ? `#${ticket.pr_number}` : ""}</td>
    <td><State value={ticket.state} /></td>
    <td className="job-updated"><time dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time></td>
  </tr>;
}

export function WorkPage({ tickets = [], ledger, loaded, error }) {
  const [filter, setFilter] = useState("all");
  const jobs = sortTickets(tickets);
  const shown = jobs.filter(filters.find(([key]) => key === filter)[2]);
  const figures = ledgerFigures(ledger);
  return <div className="page work-page">
    <header className="page-head"><h1>Work</h1></header>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading tickets</p>}
    {loaded && !jobs.length && <p className="empty">Nothing yet. <a href="#/home">Assign some work</a>, or mention the bot on an issue.</p>}
    {jobs.length > 0 && <div className="filters" role="group" aria-label="Show">
      {filters.map(([key, label, matches]) => <button key={key} type="button" aria-pressed={filter === key} onClick={() => setFilter(key)}>{label}<span>{jobs.filter(matches).length}</span></button>)}
    </div>}
    {shown.length > 0 && <div className="list-card"><table className="jobs">
      <thead><tr><th scope="col">Job</th><th scope="col" className="job-detail">Repository</th><th scope="col" className="job-detail">PR</th><th scope="col">Status</th><th scope="col" className="job-updated">Updated</th></tr></thead>
      <tbody>{shown.map(ticket => <JobRow key={ticket.id} ticket={ticket} />)}</tbody>
    </table></div>}
    {jobs.length > 0 && !shown.length && <p className="empty">No job has this status.</p>}
    {figures.length > 0 && <section className="panel ledger" aria-label="Average per merged PR">
      <h2 className="label">Average per merged PR, across {ledger.merged_prs}</h2>
      <dl>{figures.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    </section>}
  </div>;
}
