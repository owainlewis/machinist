import React, { useState } from "react";
import { Columns3, List, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { State, relativeTime } from "./display.jsx";
import { boardColumns, boardOf, jobState, newestFirst, runningTickets } from "./jobs.js";
import { jobHref } from "./routes.js";

const viewKey = "machinist-jobs-view";

function savedView() {
  try { return localStorage.getItem(viewKey) === "list" ? "list" : "board"; } catch { return "board"; }
}

function JobRow({ ticket, running }) {
  return <tr>
    <td className="job-title"><a href={jobHref(ticket.id)}>{ticket.title || ticket.id}</a></td>
    <td className="job-detail">{ticket.repo}</td>
    <td className="job-detail job-preset">{ticket.agent}</td>
    <td className="job-detail job-pr">{ticket.pr_number ? `#${ticket.pr_number}` : ""}</td>
    <td><State value={jobState(ticket, running)} /></td>
    <td className="job-updated"><time dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time></td>
  </tr>;
}

function JobList({ jobs, running }) {
  return <div className="list-card"><table className="jobs">
    <thead><tr><th scope="col">Job</th><th scope="col" className="job-detail">Repository</th><th scope="col" className="job-detail job-preset">Preset</th><th scope="col" className="job-detail job-pr">PR</th><th scope="col">Status</th><th scope="col" className="job-updated">Updated</th></tr></thead>
    <tbody>{jobs.map(ticket => <JobRow key={ticket.id} ticket={ticket} running={running.has(ticket.id)} />)}</tbody>
  </table></div>;
}

function JobCard({ ticket, state, showState }) {
  return <li className="board-card">
    <a className="board-card-title" href={jobHref(ticket.id)}>{ticket.title || ticket.id}</a>
    <div className="board-card-meta">
      <span>{ticket.repo}</span>
      {ticket.pr_number ? <span>#{ticket.pr_number}</span> : null}
      <time dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time>
    </div>
    {showState && <State value={state} />}
  </li>;
}

// JobBoard shows the same jobs as columns, so what is waiting and what is
// running can be read at a glance.
function JobBoard({ tickets, running }) {
  const board = boardOf(tickets, running);
  return <div className="board">
    {boardColumns.map(column => <section key={column.key} className="board-column" aria-labelledby={`board-${column.key}`}>
      <h2 id={`board-${column.key}`} className="board-column-head"><span>{column.label}</span><span className="board-count">{board[column.key].length}</span></h2>
      {board[column.key].length
        ? <ul className="board-cards">{board[column.key].map(job => <JobCard key={job.ticket.id} {...job} showState={column.key === "done"} />)}</ul>
        : <p className="board-empty">None</p>}
    </section>)}
  </div>;
}

export function JobsPage({ tickets = [], runners = [], loaded, error }) {
  const [view, setView] = useState(savedView);
  const jobs = newestFirst(tickets);
  const running = runningTickets(runners);
  function choose(next) {
    setView(next);
    try { localStorage.setItem(viewKey, next); } catch { /* The choice lasts for this visit. */ }
  }
  return <div className="page jobs-page">
    <header className="page-head">
      <h1>Jobs</h1>
      <div className="page-actions">
        <div className="range" role="group" aria-label="Show jobs as">
          <button type="button" aria-pressed={view === "board"} onClick={() => choose("board")}><Columns3 className="size-3.5" aria-hidden="true" />Board</button>
          <button type="button" aria-pressed={view === "list"} onClick={() => choose("list")}><List className="size-3.5" aria-hidden="true" />List</button>
        </div>
        <Button asChild><a href="#/new"><Plus className="size-4" aria-hidden="true" />New job</a></Button>
      </div>
    </header>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading jobs</p>}
    {loaded && !jobs.length && <p className="empty">No jobs yet. <a href="#/new">Start one</a>, or send <code>POST /machinist/run</code>.</p>}
    {jobs.length > 0 && (view === "board" ? <JobBoard tickets={tickets} running={running} /> : <JobList jobs={jobs} running={running} />)}
  </div>;
}
