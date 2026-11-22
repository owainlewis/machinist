import React, { Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowLeft, Check, ChevronRight, CircleAlert, CircleDashed, Download, LoaderCircle, Minus, RotateCcw } from "lucide-react";
import { api, SignedOutError } from "./api.js";
import { AgentBadges } from "./agent-badge.jsx";
import { State, relativeTime, useNow } from "./display.jsx";
import { failureReason, formatDuration, isFinished, jobState, linkParts, noPullRequest, prLink, prState } from "./jobs.js";
import { latestAction, noStepsNote, pipeline, pollEvents, runSpend, spend, spendTitle, stepTime, steps } from "./step-log.js";
import { Button } from "@/components/ui/button";

const pollMillis = 2000;
const eventPage = 128;

// useRunEvents reads every event of a run, and keeps reading while it can send more.
// PullRequest links the job's pull request and says whether it is open,
// merged or closed, and since when.
function PullRequest({ ticket, link }) {
  const state = prState(ticket);
  return <>
    <a href={link} target="_blank" rel="noreferrer">#{ticket.pr_number}</a>
    {state && <> · {state}{ticket.pr_state_at && <> <time dateTime={ticket.pr_state_at}>{relativeTime(ticket.pr_state_at)}</time></>}</>}
  </>;
}

export function useRunEvents(run) {
  const [snapshot, setSnapshot] = useState({ id: run.id, events: [], error: "", loaded: false });
  const live = pollEvents(run);
  useEffect(() => {
    let stopped = false;
    let cursor = 0;
    let timer;
    const controller = new AbortController();
    setSnapshot({ id: run.id, events: [], error: "", loaded: false });
    async function poll() {
      let retry = false;
      try {
        let more = true;
        while (more && !stopped) {
          const page = await api(`/api/v1/runs/${encodeURIComponent(run.id)}/events?after=${cursor}`, { signal: controller.signal });
          if (stopped) return;
          setSnapshot(previous => ({ id: run.id, events: [...previous.events, ...page.events], error: "", loaded: true }));
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
  return snapshot.id === run.id ? snapshot : { events: [], error: "", loaded: false };
}

const stepIcons = {
  succeeded: [Check, "text-success"],
  running: [LoaderCircle, "text-info state-working"],
  failed: [CircleAlert, "text-danger"],
  timed_out: [CircleAlert, "text-danger"],
  cancelled: [Minus, "text-muted-foreground"],
  skipped: [Minus, "text-faint"],
  pending: [CircleDashed, "text-faint"],
  unknown: [Minus, "text-faint"],
};

const stateWords = { succeeded: "Passed", running: "Running", failed: "Failed", timed_out: "Timed out", cancelled: "Cancelled", skipped: "Skipped", pending: "Waiting", unknown: "" };

// Pipeline draws the preset's steps left to right, joined in the order they
// run. Each node shows how far the latest attempt got. Choosing one opens its
// output below.
function Pipeline({ nodes, onChoose, now = Date.now() }) {
  return <section className="pipeline" aria-label="Pipeline">
    <ol>{nodes.map((step, index) => {
      const [Icon, tone] = stepIcons[step.state] || stepIcons.unknown;
      const empty = !step.output && !step.error;
      return <li key={step.name} data-state={step.state}>
        {index > 0 && <span className="pipeline-edge" aria-hidden="true" />}
        <button type="button" className="pipeline-node" disabled={empty} onClick={() => onChoose(step.name)} aria-label={`${step.name}: ${stateWords[step.state] || step.state}`}>
          <Icon className={`size-4 shrink-0 ${tone}`} aria-hidden="true" />
          <span className="pipeline-name">{step.name}</span>
          <span className="pipeline-meta">{step.state !== "skipped" && stepTime(step, now) || stateWords[step.state]}</span>
          <AgentBadges agents={step.agents} step={step.name} compact className="pipeline-agents" />
        </button>
      </li>;
    })}</ol>
  </section>;
}

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

function Step({ step, chosen, now }) {
  const [Icon, tone] = stepIcons[step.state] || stepIcons.unknown;
  const empty = !step.output && !step.error;
  const [open, setOpen] = useState(step.state === "failed" || step.state === "running");
  const item = useRef(null);
  useEffect(() => { if (step.state === "failed" || step.state === "running") setOpen(true); }, [step.state]);
  useEffect(() => {
    if (!chosen) return;
    setOpen(true);
    item.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [chosen]);
  return <li ref={item} className="step" data-state={step.state}>
    <button type="button" className="step-row" aria-expanded={open} disabled={empty} onClick={() => setOpen(value => !value)}>
      <Icon className={`size-4 shrink-0 ${tone}`} aria-hidden="true" />
      <span className="step-name">{step.name}</span>
      <span className="step-error">{step.error}</span>
      <span className="step-spend" title={spendTitle(step.cost_unknown)}>{spend(step)}</span>
      <span className="step-time">{step.state === "skipped" ? "Skipped" : stepTime(step, now)}</span>
      <ChevronRight className="step-chevron size-4" aria-hidden="true" />
    </button>
    {open && !empty && <StepOutput text={step.output || step.error} />}
  </li>;
}

function Run({ run, number, presetSteps }) {
  const { events, error, loaded } = useRunEvents(run);
  const list = steps(events, run.stages || [], run.status);
  const now = useNow(run.status === "running");
  const current = run.status === "running" ? list.find(step => step.state === "running") : null;
  const action = current ? latestAction(current.output) : "";
  const nodes = presetSteps ? pipeline(presetSteps, list, run.status) : [];
  // chosen counts each choice, so choosing the same step twice still opens it.
  const [chosen, setChosen] = useState({ name: "", times: 0 });
  const choose = name => setChosen(previous => ({ name, times: previous.times + 1 }));
  return <section className="run" aria-label={`Attempt ${number}`}>
    <div className="run-head">
      <h2>Attempt {number}</h2>
      <span>{run.status === "queued" ? "Waiting for a runner" : `${run.status.replaceAll("_", " ")} on ${run.runner || "a runner"}`}</span>
      {run.started_at && <span>{formatDuration((run.ended_at ? Date.parse(run.ended_at) : now) - Date.parse(run.started_at))}</span>}
      {runSpend(run.stages) && <span title={spendTitle((run.stages || []).some(stage => stage.cost_unknown))}>{runSpend(run.stages)}</span>}
    </div>
    {run.patch && <p className="run-patch">
      <a href={`/api/v1/runs/${encodeURIComponent(run.id)}/patch`} download><Download className="size-3.5" aria-hidden="true" />Download the change as a patch</a>
      <span>This attempt could not push its work. Apply it with <code>git am</code>.</span>
    </p>}
    {current && <p className="run-now" aria-live="polite"><span className="run-now-step">{current.name}</span>{action || "Working"}</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {nodes.length > 0 && <Pipeline nodes={nodes} onChoose={choose} now={now} />}
    {list.length > 0 ? <ol className="steps">{list.map(step => <Step key={step.name} step={step} chosen={chosen.name === step.name ? chosen.times : 0} now={now} />)}</ol> : <p className="page-note">{noStepsNote(run.status, loaded)}</p>}
  </section>;
}

// Linked shows text with its web links made into links.
function Linked({ text }) {
  return linkParts(text).map((part, index) => part.href
    ? <a key={index} href={part.href} target="_blank" rel="noreferrer">{part.href}</a>
    : <Fragment key={index}>{part.text}</Fragment>);
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

// RunAgain starts the preset again from its first step, on the same branch.
function RunAgain({ ticketID, onDone }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function rerun() {
    setBusy(true);
    setError("");
    try {
      await api(`/api/v1/tickets/${encodeURIComponent(ticketID)}/rerun`, { method: "POST" });
      await onDone?.();
    } catch (failure) {
      setError(failure.message);
    } finally {
      setBusy(false);
    }
  }
  return <div className="run-again">
    <Button variant="outline" size="sm" onClick={rerun} disabled={busy}><RotateCcw className="size-3.5" aria-hidden="true" />{busy ? "Starting" : "Run again"}</Button>
    {error && <p className="form-error" role="alert">{error}</p>}
  </div>;
}

export function JobView({ detail, onChange }) {
  const { ticket, entries = [], runs = [], steps: presetSteps = [] } = detail;
  const state = jobState(ticket, runs.some(run => run.status === "running"));
  const pr = prLink(ticket);
  const prompt = entries.find(entry => entry.kind === "request")?.body || "";
  const result = [...runs].reverse().find(run => run.report?.summary)?.report.summary || "";
  const failure = state === "failed" ? failureReason(entries) : "";
  const attempts = runs.map((run, index) => ({ run, number: index + 1 })).reverse();
  return <div className="page job-page">
    <a className="back-link" href="#/jobs"><ArrowLeft className="size-3.5" aria-hidden="true" />Jobs</a>
    <header className="job-head">
      <h1>{ticket.title || ticket.id}</h1>
      <div className="job-actions">
        <State value={state} />
        {isFinished(state) && <RunAgain ticketID={ticket.id} onDone={onChange} />}
      </div>
    </header>
    <dl className="job-facts">
      <div><dt>Repository</dt><dd>{ticket.repo}</dd></div>
      <div><dt>Preset</dt><dd>{ticket.agent}</dd></div>
      <div><dt>Pull request</dt><dd>{pr ? <PullRequest ticket={ticket} link={pr} /> : noPullRequest(state)}</dd></div>
      <div><dt>Started by</dt><dd>{ticket.created_by}</dd></div>
      <div><dt>Created</dt><dd><time dateTime={ticket.created_at}>{relativeTime(ticket.created_at)}</time></dd></div>
    </dl>
    {failure && <section className="panel job-failure" aria-labelledby="job-failure">
      <h2 id="job-failure" className="label"><CircleAlert className="size-3.5" aria-hidden="true" />Why it failed</h2>
      <p className="prose"><Linked text={failure} /></p>
    </section>}
    {result && isFinished(state) && <section className="panel" aria-labelledby="job-result"><h2 id="job-result" className="label">Result</h2><p className="prose"><Linked text={result} /></p></section>}
    {prompt && <details className="panel prompt"><summary className="label">Prompt</summary><p className="prose">{prompt}</p></details>}
    {attempts.length ? attempts.map(({ run, number }, index) => <Run key={run.id} run={run} number={number} presetSteps={index === 0 ? presetSteps : null} />) : <>
      {presetSteps.length > 0 && <Pipeline nodes={pipeline(presetSteps, [], "")} onChoose={() => {}} />}
      <p className="page-note">Waiting for a runner.</p>
    </>}
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
  return <>{error && <p className="form-error page-banner" role="alert">{error}</p>}<JobView detail={detail} onChange={refresh} /></>;
}
