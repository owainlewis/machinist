import React, { useEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, Columns3, List, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { AgentBadges } from "./agent-badge.jsx";
import { api, SignedOutError } from "./api.js";
import { State, relativeTime, useNow } from "./display.jsx";
import {
  boardColumns, boardDoneLimit, boardOf, filterJobs, formatDuration, jobState, listPageSize,
  newestFirst, orderSources, paginationOf, quickCounts, quickFilters, runningJobs, sourceLabel,
  sourcesOf, ticketsPath,
} from "./jobs.js";
import { PRChip } from "./pr-chip.jsx";
import { jobHref } from "./routes.js";
import { Source } from "./source-mark.jsx";
import { createStatusLoader } from "./status-loader.js";

const viewKey = "machinist-jobs-view";
const pollMillis = 2000;

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

function JobList({ jobs, running, pagination, onPrev, onNext }) {
  const showPagination = pagination && (pagination.totalPages > 1 || pagination.hasPrev || pagination.hasNext);
  return <>
    <div className="list-card"><table className="jobs">
      <thead><tr><th scope="col">Job</th><th scope="col" className="job-detail job-source">Source</th><th scope="col" className="job-detail">Repository</th><th scope="col" className="job-detail job-preset">Preset</th><th scope="col" className="job-pr">PR</th><th scope="col">Status</th><th scope="col" className="job-updated">Updated</th></tr></thead>
      <tbody>{jobs.map(ticket => <JobRow key={ticket.id} ticket={ticket} running={running.has(ticket.id)} />)}</tbody>
    </table></div>
    {showPagination && <nav className="pagination" aria-label="Jobs pages">
      <span className="pagination-summary">Showing {pagination.start}–{pagination.end} of {pagination.total}</span>
      <div className="pagination-controls">
        <Button type="button" variant="outline" size="sm" disabled={!pagination.hasPrev} onClick={onPrev}>
          <ChevronLeft className="size-4" aria-hidden="true" />
          Previous
        </Button>
        <span className="pagination-page">Page {pagination.page} of {pagination.totalPages}</span>
        <Button type="button" variant="outline" size="sm" disabled={!pagination.hasNext} onClick={onNext}>
          Next
          <ChevronRight className="size-4" aria-hidden="true" />
        </Button>
      </div>
    </nav>}
  </>;
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
// running can be read at a glance. Done is capped to the 20 most recent jobs.
function JobBoard({ tickets, running, progress, doneTotal, onViewList }) {
  const board = boardOf(tickets, running, { doneLimit: boardDoneLimit });
  const totalDone = typeof doneTotal === "number" ? Math.max(doneTotal, board.done.length) : board.doneTotal;
  const cappedDone = totalDone > board.done.length;
  const now = useNow(board.running.length > 0);
  return <div className="board">
    {boardColumns.map(column => {
      const cards = board[column.key];
      const count = column.key === "done" ? totalDone : cards.length;
      return <section key={column.key} className="board-column" aria-labelledby={`board-${column.key}`}>
        <h2 id={`board-${column.key}`} className="board-column-head"><span className="board-column-name"><span className="board-dot" data-column={column.key} aria-hidden="true" />{column.label}</span><span className="board-count">{count}</span></h2>
        {cards.length
          ? <ul className="board-cards">{cards.map(job => <JobCard key={job.ticket.id} {...job} showState={column.key === "done"} progress={progress.get(job.ticket.id)} now={now} />)}</ul>
          : <p className="board-empty">None</p>}
        {column.key === "done" && cappedDone && <div className="board-column-foot">
          <span className="board-column-summary">Showing {cards.length} of {totalDone}</span>
          <button type="button" className="board-view-all" onClick={onViewList}>View all in List</button>
        </div>}
      </section>;
    })}
  </div>;
}

// JobFilters are the quick filters by state and the filter by source. Each
// quick filter counts the jobs it would show.
function JobFilters({ counts, sources, source, quick, onSource, onQuick }) {
  const options = orderSources(source ? [...sources, source] : sources);
  return <div className="job-filters">
    <div className="quick-filters" role="group" aria-label="Show jobs that are">
      <button type="button" className="filter-chip" aria-pressed={!quick} onClick={() => onQuick("")}>All</button>
      {quickFilters.map(({ key, label }) => <button key={key} type="button" className="filter-chip" data-filter={key} aria-pressed={quick === key} onClick={() => onQuick(quick === key ? "" : key)}>
        {label}<span className="filter-chip-count">{counts[key] ?? 0}</span>
      </button>)}
    </div>
    <Select className="source-filter" value={source} onChange={onSource} aria-label="Source">
      <option value="">All sources</option>
      {options.map(name => <option key={name} value={name}>{sourceLabel(name)}</option>)}
    </Select>
  </div>;
}

export function JobsPage({ tickets, runners = [], loaded, error, onSignedOut, pageSize = listPageSize }) {
  const controlled = tickets !== undefined;
  const [view, setView] = useState(savedView);
  const [source, setSource] = useState("");
  const [quick, setQuick] = useState("");
  const [page, setPage] = useState(0);
  const [fetched, setFetched] = useState({ view: null, tickets: [], total: 0, offset: 0, hasMore: false, doneTotal: null, counts: null, sources: null });
  const [fetchedLoaded, setFetchedLoaded] = useState(false);
  const [fetchedError, setFetchedError] = useState("");
  const onSignedOutRef = useRef(onSignedOut);
  onSignedOutRef.current = onSignedOut;

  useEffect(() => {
    if (controlled) return undefined;
    let stopped = false;
    let timer;
    const path = ticketsPath({ view, source, quick, page, pageSize, doneLimit: boardDoneLimit });
    const loader = createStatusLoader({
      request: () => api(path),
      apply: result => {
        if (result.kind === "success") {
          const data = result.status || {};
          const list = Array.isArray(data.tickets) ? data.tickets : [];
          const total = typeof data.total === "number" ? data.total : list.length;
          const offset = typeof data.offset === "number" ? data.offset : (view === "list" ? page * pageSize : 0);
          if (view === "list" && page > 0 && list.length === 0 && total > 0) {
            setPage(Math.max(0, Math.ceil(total / pageSize) - 1));
            return;
          }
          setFetched({
            view,
            tickets: list,
            total,
            offset,
            hasMore: typeof data.has_more === "boolean" ? data.has_more : offset + list.length < total,
            doneTotal: typeof data.done_total === "number" ? data.done_total : null,
            counts: data.counts || null,
            sources: Array.isArray(data.sources) ? data.sources : null,
          });
          setFetchedError("");
          setFetchedLoaded(true);
        } else if (result.message === new SignedOutError().message) {
          onSignedOutRef.current?.();
        } else {
          setFetchedError(result.message);
        }
      },
    });
    const load = async () => {
      await loader.refresh();
      if (!stopped) timer = window.setTimeout(load, pollMillis);
    };
    load();
    return () => { stopped = true; loader.cancel(); window.clearTimeout(timer); };
  }, [controlled, view, source, quick, page, pageSize]);

  const progress = runningJobs(runners);
  const running = new Set(progress.keys());

  function choose(next) {
    setView(next);
    setPage(0);
    try { localStorage.setItem(viewKey, next); } catch { /* The choice lasts for this visit. */ }
    if (typeof document !== "undefined") document.documentElement.scrollTop = 0;
  }

  function changeSource(next) {
    setSource(next);
    setPage(0);
  }

  function changeQuick(next) {
    setQuick(next);
    setPage(0);
  }

  function goToPage(next) {
    const maxPage = Math.max(0, pagination.totalPages - 1);
    setPage(Math.min(maxPage, Math.max(0, next)));
    if (typeof document !== "undefined") document.documentElement.scrollTop = 0;
  }

  const isLoaded = controlled ? loaded : fetchedLoaded;
  const pageError = controlled ? error : fetchedError;
  const viewReady = controlled || fetched.view === view;

  let counts;
  let sources;
  let boardTickets;
  let boardDoneTotal;
  let listJobs;
  let pagination;
  let hasAnyJobs;

  if (controlled) {
    const all = tickets;
    counts = quickCounts(all, source, running);
    sources = sourcesOf(all);
    const shown = filterJobs(all, { source, quick }, running);
    boardTickets = shown;
    boardDoneTotal = undefined;
    const ordered = newestFirst(shown);
    const total = ordered.length;
    const maxPage = Math.max(0, Math.ceil(total / pageSize) - 1);
    const currentPage = Math.min(page, maxPage);
    const offset = currentPage * pageSize;
    listJobs = ordered.slice(offset, offset + pageSize);
    pagination = paginationOf({ total, offset, count: listJobs.length, pageSize });
    hasAnyJobs = all.length > 0;
  } else {
    const activeList = viewReady ? fetched.tickets : [];
    counts = fetched.counts || quickCounts(fetched.tickets, source, running);
    sources = fetched.sources || sourcesOf(fetched.tickets);
    boardTickets = activeList;
    boardDoneTotal = fetched.doneTotal;
    listJobs = newestFirst(activeList);
    pagination = paginationOf({
      total: fetched.total,
      offset: fetched.offset,
      count: listJobs.length,
      pageSize,
      hasMore: fetched.hasMore,
    });
    const totalFromCounts = fetched.counts
      ? Object.values(fetched.counts).reduce((sum, n) => sum + (n || 0), 0)
      : 0;
    hasAnyJobs = Boolean(source || quick) || fetched.total > 0 || fetched.tickets.length > 0 || totalFromCounts > 0 || sources.length > 0;
  }

  const shownCount = view === "board" ? boardTickets.length : listJobs.length;

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
    {pageError && <p className="form-error" role="alert">{pageError}</p>}
    {!isLoaded && !pageError && <p className="page-note" role="status">Loading jobs</p>}
    {isLoaded && !hasAnyJobs && !pageError && <p className="empty">No jobs yet. <a href="#/new">Start one</a>, or send <code>POST /machinist/run</code>.</p>}
    {hasAnyJobs && <JobFilters counts={counts} sources={sources} source={source} quick={quick} onSource={changeSource} onQuick={changeQuick} />}
    {hasAnyJobs && isLoaded && viewReady && shownCount === 0 && <p className="empty">No jobs match these filters. <button type="button" className="link-button" onClick={() => { changeSource(""); changeQuick(""); }}>Show all jobs</button></p>}
    {viewReady && shownCount > 0 && (view === "board"
      ? <JobBoard tickets={boardTickets} running={running} progress={progress} doneTotal={boardDoneTotal} onViewList={() => choose("list")} />
      : <JobList jobs={listJobs} running={running} pagination={pagination} onPrev={() => goToPage(page - 1)} onNext={() => goToPage(page + 1)} />)}
  </div>;
}
