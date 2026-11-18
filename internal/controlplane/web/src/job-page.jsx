import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowLeft, Check, ChevronRight, CircleAlert, LoaderCircle, Minus } from "lucide-react";
import { api, SignedOutError } from "./api.js";
import { State, relativeTime } from "./display.jsx";
import { formatDuration, isFinished, jobState, prLink } from "./jobs.js";
import { pollEvents, steps } from "./step-log.js";

const pollMillis = 2000;
const eventPage = 128;

// useRunEvents reads every event of a run, and keeps reading while it can send more.
export function useRunEvents(run) {
  const [snapshot, setSnapshot] = useState({ id: run.id, events: [], error: "" });
  const live = pollEvents(run);
  useEffect(() => {
    let stopped = false;
    let cursor = 0;
    let timer;
    const controller = new AbortController();
    setSnapshot({ id: run.id, events: [], error: "" });
    async function poll() {
      let retry = false;
      try {
        let more = true;
        while (more && !stopped) {
          const page = await api(`/api/v1/runs/${encodeURIComponent(run.id)}/events?after=${cursor}`, { signal: controller.signal });
          if (stopped) return;
          setSnapshot(previous => ({ id: run.id, events: [...previous.events, ...page.events], error: "" }));
          cursor = page.cursor;
          more = page.events.length === eventPage;
        }
      } catch (failure) {
        if (stopped) return;
        retry = !(failure instanceof SignedOutError);
        setSnapshot(previous => ({ ...previous, error: failure.message }));
      }
      if (!stopped && (retry || live)) timer = window.setTimeout(poll, pollMillis);
    }
    poll();
    return () => { stopped = true; window.clearTimeout(timer); controller.abort(); };
  }, [run.id, live]);
  return snapshot.id === run.id ? snapshot : { events: [], error: "" };
}

const stepIcons = {
  succeeded: [Check, "text-success"],
  running: [LoaderCircle, "text-info state-working"],
  failed: [CircleAlert, "text-danger"],
  timed_out: [CircleAlert, "text-danger"],
  cancelled: [Minus, "text-muted-foreground"],
  skipped: [Minus, "text-faint"],
  unknown: [Minus, "text-faint"],
};

// The output pane follows new lines while the reader is at its end.
function StepOutput({ text }) {
  const pane = useRef(null);
  const following = useRef(true);
  useLayoutEffect(() => {
    if (pane.current && following.current) pane.current.scrollTop = pane.current.scrollHeight;
  }, [text]);
  const scrolled = () => {
    const node = pane.current;
    following.current = node.scrollHeight - node.scrollTop - node.clientHeight < 24;
  };
  return <pre ref={pane} className="step-output" onScroll={scrolled}>{text}</pre>;
}

function Step({ step }) {
  const [Icon, tone] = stepIcons[step.state] || stepIcons.unknown;
  const empty = !step.output && !step.error;
  const [open, setOpen] = useState(step.state === "failed" || step.state === "running");
  useEffect(() => { if (step.state === "failed" || step.state === "running") setOpen(true); }, [step.state]);
  return <li className="step" data-state={step.state}>
    <button type="button" className="step-row" aria-expanded={open} disabled={empty} onClick={() => setOpen(value => !value)}>
      <Icon className={`size-4 shrink-0 ${tone}`} aria-hidden="true" />
      <span className="step-name">{step.name}</span>
      <span className="step-error">{step.error}</span>
      <span className="step-time">{step.state === "skipped" ? "Skipped" : formatDuration(step.duration_millis)}</span>
      <ChevronRight className="step-chevron size-4" aria-hidden="true" />
    </button>
    {open && !empty && <StepOutput text={step.output || step.error} />}
  </li>;
}

function Run({ run, number }) {
  const { events, error } = useRunEvents(run);
  const list = steps(events, run.stages || [], run.status);
  return <section className="run" aria-label={`Attempt ${number}`}>
    <div className="run-head">
      <h2>Attempt {number}</h2>
      <span>{run.status === "queued" ? "Waiting for a runner" : `${run.status.replaceAll("_", " ")} on ${run.runner || "a runner"}`}</span>
      {run.started_at && <span>{formatDuration((run.ended_at ? Date.parse(run.ended_at) : Date.now()) - Date.parse(run.started_at))}</span>}
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    {list.length > 0 ? <ol className="steps">{list.map(step => <Step key={step.name} step={step} />)}</ol> : <p className="page-note">{run.status === "queued" ? "No step has started." : "Loading steps"}</p>}
  </section>;
}

function History({ entries }) {
  const shown = entries.filter(entry => entry.kind !== "request");
  if (!shown.length) return null;
  return <section className="panel history" aria-labelledby="job-history">
    <h2 id="job-history" className="label">History</h2>
    <ol>{shown.map(entry => <li key={entry.id}>
      <div className="history-meta"><strong>{entry.author}</strong><time dateTime={entry.created_at}>{relativeTime(entry.created_at)}</time></div>
      <p>{entry.body}</p>
    </li>)}</ol>
  </section>;
}

export function JobView({ detail }) {
  const { ticket, entries = [], runs = [] } = detail;
  const state = jobState(ticket, runs.some(run => run.status === "running"));
  const pr = prLink(ticket);
  const prompt = entries.find(entry => entry.kind === "request")?.body || "";
  const result = [...runs].reverse().find(run => run.report?.summary)?.report.summary || "";
  const attempts = runs.map((run, index) => ({ run, number: index + 1 })).reverse();
  return <div className="page job-page">
    <a className="back-link" href="#/jobs"><ArrowLeft className="size-3.5" aria-hidden="true" />Jobs</a>
    <header className="job-head">
      <h1>{ticket.title || ticket.id}</h1>
      <State value={state} />
    </header>
    <dl className="job-facts">
      <div><dt>Repository</dt><dd>{ticket.repo}</dd></div>
      <div><dt>Preset</dt><dd>{ticket.agent}</dd></div>
      <div><dt>Pull request</dt><dd>{pr ? <a href={pr} target="_blank" rel="noreferrer">#{ticket.pr_number}</a> : "None yet"}</dd></div>
      <div><dt>Started by</dt><dd>{ticket.created_by}</dd></div>
      <div><dt>Created</dt><dd><time dateTime={ticket.created_at}>{relativeTime(ticket.created_at)}</time></dd></div>
    </dl>
    {result && isFinished(state) && <section className="panel" aria-labelledby="job-result"><h2 id="job-result" className="label">Result</h2><p className="prose">{result}</p></section>}
    {prompt && <details className="panel prompt"><summary className="label">Prompt</summary><p className="prose">{prompt}</p></details>}
    {attempts.length ? attempts.map(({ run, number }) => <Run key={run.id} run={run} number={number} />) : <p className="page-note">Waiting for a runner.</p>}
    <History entries={entries} />
  </div>;
}

// The app keys this by job, so a reply for a job someone has left never shows under another.
export function JobPage({ jobID, onSignedOut }) {
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState("");
  const refresh = useCallback(async () => {
    try {
      setDetail(await api(`/api/v1/tickets/${encodeURIComponent(jobID)}`));
      setError("");
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    }
  }, [jobID, onSignedOut]);
  useEffect(() => {
    let stopped = false;
    let timer;
    const load = async () => {
      await refresh();
      if (!stopped) timer = window.setTimeout(load, pollMillis);
    };
    load();
    return () => { stopped = true; window.clearTimeout(timer); };
  }, [refresh]);
  if (!detail) return <div className="page">
    <a className="back-link" href="#/jobs"><ArrowLeft className="size-3.5" aria-hidden="true" />Jobs</a>
    {error ? <p className="form-error" role="alert">{error}</p> : <p className="page-note" role="status">Loading job</p>}
  </div>;
  return <>{error && <p className="form-error page-banner" role="alert">{error}</p>}<JobView detail={detail} /></>;
}
