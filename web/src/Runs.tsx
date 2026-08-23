import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, AlertTriangle, ArrowLeft, CheckCircle2, CircleDot, Clock3, Columns3, GitBranch, GitMerge, RotateCcw, Rows3, StopCircle, TerminalSquare } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api";
import { duration, eventSummary, timeAgo } from "./format";
import type { Attempt, AttemptEvent, Run, RunState, Session } from "./types";
import { EmptyState, ErrorState, InlineError, LoadingState, StaleBanner, StatusBadge, ViewHeader } from "./ui";

export type RunViewMode = "table" | "kanban";

interface RunHistory {
  items: Run[];
  cursor: string | null;
  headCursor: string | null;
}

const runHistoryKey = ["run-history"] as const;

export function RunsView({ mode, onMode, onRun }: { mode: RunViewMode; onMode: (mode: RunViewMode) => void; onRun: (id: string) => void }) {
  const client = useQueryClient();
  const cachedHistory = client.getQueryData<RunHistory>(runHistoryKey);
  const [history, setHistory] = useState<Run[]>(cachedHistory?.items ?? []);
  const [historyCursor, setHistoryCursor] = useState<string | null>(cachedHistory?.cursor ?? null);
  const [, setAttentionClock] = useState(0);
  const previousHeadCursor = useRef<string | null>(cachedHistory?.headCursor ?? null);
  const query = useQuery({ queryKey: ["runs", "head"], queryFn: () => api.runs(), refetchInterval: 5_000 });
  const headIDs = new Set(query.data?.runs.map((run) => run.id) ?? []);
  const activeHistoricalIDs = history
    .filter((run) => !headIDs.has(run.id) && activeRunState(run.state))
    .map((run) => run.id)
    .sort();
  const activeHistory = useQueries({
    queries: activeHistoricalIDs.map((id) => ({
      queryKey: ["run-history", "active", id],
      queryFn: async () => (await api.run(id)).run,
      refetchInterval: (activeQuery: { state: { data?: Run } }) => activeQuery.state.data && !activeRunState(activeQuery.state.data.state) ? false : 5_000,
      refetchOnWindowFocus: false,
    })),
    combine: (results) => ({
      data: results.flatMap((result) => result.data ? [result.data] : []),
      error: results.find((result) => result.error)?.error,
      isFetching: results.some((result) => result.isFetching),
      dataUpdatedAt: Math.max(0, ...results.map((result) => result.dataUpdatedAt)),
    }),
  });
  const refreshedHistory = useMemo(
    () => updateRuns(history, activeHistory.data),
    [activeHistory.data, history],
  );
  const loadHistory = useMutation({
    mutationFn: ({ cursor }: { cursor: string; headCursor: string | null }) => api.runs(cursor),
    onSuccess: (page, request) => {
      setHistory((current) => mergeRuns(page.runs, updateRuns(current, activeHistory.data)));
      if (previousHeadCursor.current === request.headCursor) setHistoryCursor(page.next_cursor);
    },
  });
  useEffect(() => {
    if (!query.data) return;
    if (previousHeadCursor.current !== query.data.next_cursor) setHistoryCursor(query.data.next_cursor);
    previousHeadCursor.current = query.data.next_cursor;
  }, [query.data]);
  useEffect(() => {
    client.setQueryData<RunHistory>(runHistoryKey, {
      items: refreshedHistory,
      cursor: historyCursor,
      headCursor: previousHeadCursor.current,
    });
  }, [client, historyCursor, query.data, refreshedHistory]);
  const mergedItems = mergeRuns(query.data?.runs ?? [], refreshedHistory);
  const nextAttentionExpiry = terminalAttentionExpiry(mergedItems);
  useEffect(() => {
    if (nextAttentionExpiry === null) return;
    const timer = window.setTimeout(
      () => setAttentionClock((value) => value + 1),
      Math.max(1, Math.min(nextAttentionExpiry - Date.now() + 1, 2_147_483_647)),
    );
    return () => window.clearTimeout(timer);
  }, [nextAttentionExpiry]);
  if (query.isPending) return <LoadingState label="Loading Work" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const items = mergedItems.map(withCurrentAttention);
  const error = loadHistory.error ?? activeHistory.error;
  return <div className="page page-run">
    <ViewHeader title="Work" fetching={query.isFetching || activeHistory.isFetching || loadHistory.isPending} updatedAt={Math.max(query.dataUpdatedAt, activeHistory.dataUpdatedAt)} onRefresh={() => void query.refetch()} />
    {error && <StaleBanner error={error} />}
    <div className="view-toolbar"><p>Follow every software run from queue to completion.</p><ViewSwitch mode={mode} onMode={onMode} /></div>
    {!items.length ? <EmptyState icon={<Rows3 size={22} />} title="No work yet" description="Run a Task now or wait for its next schedule." /> : mode === "table" ? <RunTable items={items} onRun={onRun} /> : <RunBoard items={items} onRun={onRun} />}
    {historyCursor && <div className="load-more-row"><button className="button button-secondary" disabled={loadHistory.isPending} onClick={() => loadHistory.mutate({ cursor: historyCursor, headCursor: previousHeadCursor.current })}>{loadHistory.isPending ? "Loading…" : "Load more Runs"}</button></div>}
  </div>;
}

function activeRunState(state: RunState): boolean {
  return state === "blocked" || state === "queued" || state === "running";
}

function attentionExpiresAt(run: Run): number | null {
  if (!run.needs_attention || (run.state !== "failed" && run.state !== "partial") || !run.terminal_at) return null;
  const terminal = Date.parse(run.terminal_at);
  return Number.isFinite(terminal) ? terminal + 24 * 60 * 60 * 1000 : null;
}

function terminalAttentionExpiry(runs: Run[]): number | null {
  const now = Date.now();
  const future = runs.map(attentionExpiresAt).filter((value): value is number => value !== null && value > now);
  return future.length ? Math.min(...future) : null;
}

function withCurrentAttention(run: Run): Run {
  const expiry = attentionExpiresAt(run);
  return expiry !== null && expiry <= Date.now() ? { ...run, needs_attention: false } : run;
}

function mergeRuns(primary: Run[], secondary: Run[]): Run[] {
  const primaryIDs = new Set(primary.map((run) => run.id));
  return [...primary, ...secondary.filter((run) => !primaryIDs.has(run.id))];
}

function updateRuns(current: Run[], updates: Run[]): Run[] {
  const byID = new Map(updates.map((run) => [run.id, run]));
  return current.map((run) => byID.get(run.id) ?? run);
}

function ViewSwitch({ mode, onMode }: { mode: RunViewMode; onMode: (mode: RunViewMode) => void }) {
  return <div className="run-view-switcher" aria-label="Work view"><button aria-pressed={mode === "kanban"} onClick={() => onMode("kanban")}><Columns3 size={14} /> Board</button><button aria-pressed={mode === "table"} onClick={() => onMode("table")}><Rows3 size={14} /> Table</button></div>;
}

function RunTable({ items, onRun }: { items: Run[]; onRun: (id: string) => void }) {
  return <div className="run-table panel"><div className="run-table-row run-table-head"><span>Task</span><span>Progress</span><span>Source</span><span>State</span><span>Started</span><span>Duration</span></div>{items.map((run) => <button className="run-table-row" key={run.id} onClick={() => onRun(run.id)}><span className="run-name"><strong>{run.task.name}</strong><small>{run.id.slice(0, 8)}</small></span><span><Progress run={run} /></span><span className="capitalize">{run.source.replace("_", " ")}</span><span><StatusBadge state={run.state} /></span><span>{timeAgo(run.admitted_at)}</span><span>{run.terminal_at ? duration(run.admitted_at, run.terminal_at) : "In progress"}</span></button>)}</div>;
}

const boardColumns: Array<{ key: string; label: string; hint: string }> = [
  { key: "queued", label: "Queued", hint: "Waiting to start" },
  { key: "running", label: "Running", hint: "Agents at work" },
  { key: "attention", label: "Blocked", hint: "Needs attention" },
  { key: "done", label: "Done", hint: "Finished work" },
];

function RunBoard({ items, onRun }: { items: Run[]; onRun: (id: string) => void }) {
  const counts = boardColumns.map((column) => ({ ...column, count: items.filter((item) => runInBoardColumn(item, column.key)).length }));
  return <>
    <section className="work-summary" aria-label="Work summary">
      {counts.map((column) => <div className={`work-summary-item work-state-${column.key}`} key={column.key}><BoardIcon column={column.key} /><span>{column.label}</span><strong>{column.count}</strong></div>)}
    </section>
    <div className="work-board">
      {boardColumns.map((column) => {
        const values = items.filter((item) => runInBoardColumn(item, column.key));
        return <section className={`work-column work-state-${column.key}`} key={column.key} aria-labelledby={`work-column-${column.key}`}>
          <header><span className="work-column-icon"><BoardIcon column={column.key} /></span><span><strong id={`work-column-${column.key}`}>{column.label}</strong><small>{column.hint}</small></span><b>{values.length}</b></header>
          <div className="work-column-list">
            {values.map((run) => <RunBoardCard key={run.id} run={run} onClick={() => onRun(run.id)} />)}
            {!values.length && <p className="work-column-empty">Nothing here</p>}
          </div>
        </section>;
      })}
    </div>
  </>;
}

function runInBoardColumn(run: Run, column: string): boolean {
  if (column === "attention") return run.needs_attention;
  if (run.needs_attention) return false;
  if (column === "running") return run.state === "running";
  if (column === "queued") return run.state === "queued" || run.state === "blocked";
  return run.state === "succeeded" || run.state === "cancelled" || run.state === "failed" || run.state === "partial";
}

function BoardIcon({ column }: { column: string }) {
  if (column === "attention") return <AlertCircle size={15} />;
  if (column === "running") return <CircleDot size={15} />;
  if (column === "queued") return <Clock3 size={15} />;
  return <CheckCircle2 size={15} />;
}

function RunBoardCard({ run, onClick }: { run: Run; onClick: () => void }) {
  const repositories = run.task.repositories ?? [];
  const runtime = run.execution.runtime || run.task.runtime;
  return <button className={`work-card work-card-${run.state}`} onClick={onClick} aria-label={`${run.task.name}, ${run.state}, ${run.session_count} sessions`}>
    <span className="work-card-top"><StatusBadge state={run.state} /><small>{timeAgo(run.updated_at || run.admitted_at)}</small></span>
    <strong>{run.task.name}</strong>
    <span className="work-card-tags">
      {repositories.slice(0, 2).map((repository) => <span className="work-chip" key={repository.id}><GitBranch size={11} />{repositoryName(repository.remote_identity)}</span>)}
      {repositories.length > 2 && <span className="work-chip">+{repositories.length - 2}</span>}
      <span className="work-chip"><TerminalSquare size={11} />{runtime}</span>
      {run.task.pipeline && <span className="work-chip"><GitMerge size={11} />{run.task.pipeline.stages.length} stage{run.task.pipeline.stages.length === 1 ? "" : "s"}</span>}
    </span>
    <Progress run={run} />
    <span className="work-card-foot"><span>{run.session_count} session{run.session_count === 1 ? "" : "s"}</span><span>{run.source.replace("_", " ")}</span></span>
  </button>;
}

function repositoryName(identity: string): string {
  const parts = identity.replace(/\.git$/, "").split("/").filter(Boolean);
  return parts.at(-1) ?? identity;
}

function Progress({ run }: { run: Run }) {
  const complete = successfulSessions(run) + run.needs_input_count + run.failed_count + run.cancelled_count;
  const percent = run.session_count ? Math.round((complete / run.session_count) * 100) : 0;
  return <span className="session-progress"><span><i style={{ width: `${percent}%` }} /></span><small>{complete}/{run.session_count}</small></span>;
}

function successfulSessions(run: Run): number {
  return run.succeeded_count + run.ready_count + run.no_change_count;
}

export function RunDetailView({ id, onBack }: { id: string; onBack: () => void }) {
  const client = useQueryClient();
  const query = useQuery({ queryKey: ["runs", id], queryFn: () => api.run(id), refetchInterval: 3_000 });
  const cancel = useMutation({ mutationFn: () => api.cancelRun(id), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["runs"] }); } });
  const retry = useMutation({ mutationFn: (sessionId: string) => api.retrySession(id, sessionId), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["runs"] }); } });
  const cancelSession = useMutation({ mutationFn: (sessionId: string) => api.cancelSession(id, sessionId), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["runs"] }); } });
  if (query.isPending) return <LoadingState label="Loading Run" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const { run, sessions } = query.data;
  const execution = run.execution.backend === "persistent"
    ? "Automatic persistent Worker"
    : `Cloud Run · ${run.execution.provider} / ${run.execution.model}`;
  return <div className="page run-detail-clean">
    <button className="back-link" onClick={onBack}><ArrowLeft size={14} /> Work</button>
    <div className="detail-heading run-detail-heading"><div><span className="eyebrow">{run.source.replace("_", " ")} · {run.id.slice(0, 8)}</span><h1>{run.task.name}</h1><p>{run.session_count} repository session{run.session_count === 1 ? "" : "s"} · {execution} · started {timeAgo(run.admitted_at)}</p></div><div className="detail-actions"><StatusBadge state={run.state} />{run.active_count > 0 && <button className="button button-danger-secondary" disabled={cancel.isPending} onClick={() => cancel.mutate()}><StopCircle size={14} /> Cancel</button>}</div></div>
    <InlineError error={cancel.error ?? retry.error ?? cancelSession.error} />
    <section className="run-summary-strip"><div><span>Pipeline</span><strong>{run.task.pipeline?.name ?? "Single agent"}</strong></div><div><span>Stages</span><strong>{run.task.pipeline?.stages.length ?? 1}</strong></div><div><span>Completed</span><strong>{successfulSessions(run)}</strong></div><div><span>Duration</span><strong>{run.terminal_at ? duration(run.admitted_at, run.terminal_at) : "Active"}</strong></div></section>
    <section className="panel session-panel"><div className="panel-heading"><h2>Sessions</h2><span>{sessions?.length ?? 0}</span></div>{(sessions ?? []).map((session) => <SessionRow key={session.id} session={session} onRetry={() => retry.mutate(session.id)} onCancel={() => cancelSession.mutate(session.id)} />)}</section>
    <details className="prompt-panel"><summary>Task snapshot</summary><pre>{run.task.prompt}</pre></details>
  </div>;
}

function SessionRow({ session, onRetry, onCancel }: { session: Session; onRetry: () => void; onCancel: () => void }) {
  const active = ["blocked", "queued", "preparing", "running"].includes(session.state);
  const [open, setOpen] = useState(false);
  return <details className="session-row" onToggle={(event) => setOpen(event.currentTarget.open)}><summary><span className="session-repo"><strong>{session.repository_identity}</strong><small>{session.assigned_worker_id ? `Worker ${session.assigned_worker_id}` : session.blocked_reason ?? "Waiting for a Worker"}</small></span><StatusBadge state={session.state} /><span className="session-time">{session.terminal_at && session.started_at ? duration(session.started_at, session.terminal_at) : session.started_at ? "Active" : "Waiting"}</span></summary>{open && <div className="session-detail-body">{session.failure_reason && <p className="session-failure"><AlertTriangle size={14} /> {session.failure_reason}</p>}<div className="stage-run-list">{(session.stages ?? []).map((stage, index) => <div className={`stage-run stage-run-${stage.state}`} key={stage.position}><span>{index + 1}</span><div><strong>{stage.name}</strong><small>{stage.state}{stage.started_at && stage.completed_at ? ` · ${duration(stage.started_at, stage.completed_at)}` : ""}</small></div><StatusBadge state={stage.state} /></div>)}</div>{session.result && <pre>{session.result}</pre>}{(session.attempts ?? []).map((attempt) => <AttemptStream key={attempt.id} attempt={attempt} />)}<div className="session-detail-actions"><span>{session.attempts?.length ?? 0} attempt{session.attempts?.length === 1 ? "" : "s"}</span>{active && <button className="button button-danger-secondary" onClick={onCancel}><StopCircle size={14} /> Cancel session</button>}{(session.state === "failed" || session.state === "cancelled") && <button className="button button-secondary" onClick={onRetry}><RotateCcw size={14} /> Retry session</button>}</div></div>}</details>;
}

function AttemptStream({ attempt }: { attempt: Attempt }) {
  const active = attempt.state === "preparing" || attempt.state === "running";
  const client = useQueryClient();
  const eventKey = ["attempt-events", attempt.id] as const;
  const query = useQuery({
    queryKey: eventKey,
    queryFn: () => loadAttemptEvents(attempt.id, client.getQueryData<AttemptEvent[]>(eventKey) ?? []),
    refetchInterval: active ? 2_000 : false,
  });
  const wasActive = useRef(active);
  const refetch = query.refetch;
  useEffect(() => {
    const becameTerminal = wasActive.current && !active;
    wasActive.current = active;
    if (becameTerminal) void refetch();
  }, [active, refetch]);
  const events = (query.data ?? []).flatMap((event) => {
    const summary = eventSummary(event);
    return summary ? [{ event, summary }] : [];
  });
  return <section className="attempt-stream"><header><strong>Attempt {attempt.attempt_number}</strong><StatusBadge state={attempt.state} /></header>{query.error && <InlineError error={query.error} />}{query.isPending ? <p className="quiet-empty">Loading attempt output…</p> : events.length === 0 ? <p className="quiet-empty">No runtime output recorded.</p> : <ol className="attempt-events">{events.map(({ event, summary }) => <li key={event.sequence}><span><strong>{summary.label}</strong><time dateTime={event.server_time}>{new Date(event.server_time).toLocaleTimeString()}</time></span><p>{summary.text}</p></li>)}</ol>}</section>;
}

async function loadAttemptEvents(attemptID: string, current: AttemptEvent[]): Promise<AttemptEvent[]> {
  const events = [...current];
  let after = events.at(-1)?.sequence ?? -1;
  for (;;) {
    const requestedAfter = after;
    const page = await api.events(attemptID, after);
    for (const event of page.events) {
      if (event.sequence > after) {
        events.push(event);
        after = event.sequence;
      }
    }
    if (!page.has_more) return events;
    if (page.next_after <= requestedAfter) throw new Error("Attempt event pagination did not advance.");
    after = page.next_after;
  }
}
