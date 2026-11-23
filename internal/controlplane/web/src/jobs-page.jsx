import React, { useState } from "react";
import { Columns3, List, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { AgentBadges } from "./agent-badge.jsx";
import { State, relativeTime, useNow } from "./display.jsx";
import { boardColumns, boardOf, filterJobs, formatDuration, jobState, newestFirst, quickCounts, quickFilters, runningJobs, sourceLabel, sourcesOf } from "./jobs.js";
import { PRChip } from "./pr-chip.jsx";
import { jobHref } from "./routes.js";
import { Source } from "./source-mark.jsx";

const viewKey = "machinist-jobs-view";

function savedView() {
  try { return localStorage.getItem(viewKey) === "list" ? "list" : "board"; } catch { return "board"; }
}

function JobRow({ ticket, running }) {
  return <tr>
    <td className="job-title"><a href={jobHref(ticket.id)}>{ticket.title || ticket.id}</a></td>
    <td className="job-detail job-source"><Source source={ticket.source} /></td>
    <td className="job-detail">{ticket.repo}</td>
    <td className="job-detail job-preset">{ticket.agent}</td>
    <td className="job-pr"><PRChip ticket={ticket} /></td>
    <td><State value={jobState(ticket, running)} /></td>
    <td className="job-updated"><time dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time></td>
  </tr>;
}

function JobList({ jobs, running }) {
  return <div className="list-card"><table className="jobs">
    <thead><tr><th scope="col">Job</th><th scope="col" className="job-detail job-source">Source</th><th scope="col" className="job-detail">Repository</th><th scope="col" className="job-detail job-preset">Preset</th><th scope="col" className="job-pr">PR</th><th scope="col">Status</th><th scope="col" className="job-updated">Updated</th></tr></thead>
    <tbody>{jobs.map(ticket => <JobRow key={ticket.id} ticket={ticket} running={running.has(ticket.id)} />)}</tbody>
  </table></div>;
}

// NowDoing is the step a running job is on, how long it has been on it, and
// the last thing its agent did.
function NowDoing({ job, now }) {
  const since = Date.parse(job.step_started_at || job.started_at);
  return <div className="board-card-now">
    {job.step && <span className="board-card-step">{job.step}{Number.isFinite(since) && <> · {formatDuration(now - since)}</>}</span>}
    <AgentBadges agents={job.agents} />
    {job.action && <span className="board-card-action" title={job.action}>{job.action}</span>}
  </div>;
}

function JobCard({ ticket, state, showState, progress, now }) {
  return <li className="board-card" data-state={state}>
    <a className="board-card-title" href={jobHref(ticket.id)}>{ticket.title || ticket.id}</a>
    {state === "running" && progress && (progress.step || progress.action) && <NowDoing job={progress} now={now} />}
    <div className="board-card-meta">
      <Source source={ticket.source} compact />
      <span>{ticket.repo}</span>
      <PRChip ticket={ticket} />
      <time dateTime={ticket.updated_at}>{relativeTime(ticket.updated_at)}</time>
    </div>
    {showState && <State value={state} />}
  </li>;
}

// JobBoard shows the same jobs as columns, so what is waiting and what is
// running can be read at a glance.
function JobBoard({ tickets, running, progress }) {
  const board = boardOf(tickets, running);
  const now = useNow(board.running.length > 0);
  return <div className="board">
    {boardColumns.map(column => <section key={column.key} className="board-column" aria-labelledby={`board-${column.key}`}>
      <h2 id={`board-${column.key}`} className="board-column-head"><span className="board-column-name"><span className="board-dot" data-column={column.key} aria-hidden="true" />{column.label}</span><span className="board-count">{board[column.key].length}</span></h2>
      {board[column.key].length
        ? <ul className="board-cards">{board[column.key].map(job => <JobCard key={job.ticket.id} {...job} showState={column.key === "done"} progress={progress.get(job.ticket.id)} now={now} />)}</ul>
        : <p className="board-empty">None</p>}
    </section>)}
  </div>;
}

// JobFilters are the quick filters by state and the filter by source. Each
// quick filter counts the jobs it would show.
function JobFilters({ tickets, running, source, quick, onSource, onQuick }) {
  const counts = quickCounts(tickets, source, running);
  const sources = sourcesOf(tickets);
  if (source && !sources.includes(source)) sources.push(source);
  return <div className="job-filters">
    <div className="quick-filters" role="group" aria-label="Show jobs that are">
      <button type="button" className="filter-chip" aria-pressed={!quick} onClick={() => onQuick("")}>All</button>
      {quickFilters.map(({ key, label }) => <button key={key} type="button" className="filter-chip" data-filter={key} aria-pressed={quick === key} onClick={() => onQuick(quick === key ? "" : key)}>
        {label}<span className="filter-chip-count">{counts[key]}</span>
      </button>)}
    </div>
    <Select className="source-filter" value={source} onChange={onSource} aria-label="Source">
      <option value="">All sources</option>
      {sources.map(name => <option key={name} value={name}>{sourceLabel(name)}</option>)}
    </Select>
  </div>;
}

export function JobsPage({ tickets = [], runners = [], loaded, error }) {
  const [view, setView] = useState(savedView);
  const [source, setSource] = useState("");
  const [quick, setQuick] = useState("");
  const progress = runningJobs(runners);
  const running = new Set(progress.keys());
  const shown = filterJobs(tickets, { source, quick }, running);
  const jobs = newestFirst(shown);
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
    {loaded && !tickets.length && <p className="empty">No jobs yet. <a href="#/new">Start one</a>, or send <code>POST /machinist/run</code>.</p>}
    {tickets.length > 0 && <JobFilters tickets={tickets} running={running} source={source} quick={quick} onSource={setSource} onQuick={setQuick} />}
    {tickets.length > 0 && !jobs.length && <p className="empty">No jobs match these filters. <button type="button" className="link-button" onClick={() => { setSource(""); setQuick(""); }}>Show all jobs</button></p>}
    {jobs.length > 0 && (view === "board" ? <JobBoard tickets={shown} running={running} progress={progress} /> : <JobList jobs={jobs} running={running} />)}
  </div>;
}
