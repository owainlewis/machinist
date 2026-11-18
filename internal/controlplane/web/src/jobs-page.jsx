import React from "react";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { State, relativeTime } from "./display.jsx";
import { jobState, newestFirst, runningTickets } from "./jobs.js";
import { jobHref } from "./routes.js";

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

export function JobsPage({ tickets = [], runners = [], loaded, error }) {
  const jobs = newestFirst(tickets);
  const running = runningTickets(runners);
  return <div className="page jobs-page">
    <header className="page-head"><h1>Jobs</h1><Button asChild><a href="#/new"><Plus className="size-4" aria-hidden="true" />New job</a></Button></header>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading jobs</p>}
    {loaded && !jobs.length && <p className="empty">No jobs yet. <a href="#/new">Start one</a>, or send <code>POST /machinist/run</code>.</p>}
    {jobs.length > 0 && <div className="list-card"><table className="jobs">
      <thead><tr><th scope="col">Job</th><th scope="col" className="job-detail">Repository</th><th scope="col" className="job-detail job-preset">Preset</th><th scope="col" className="job-detail job-pr">PR</th><th scope="col">Status</th><th scope="col" className="job-updated">Updated</th></tr></thead>
      <tbody>{jobs.map(ticket => <JobRow key={ticket.id} ticket={ticket} running={running.has(ticket.id)} />)}</tbody>
    </table></div>}
  </div>;
}
