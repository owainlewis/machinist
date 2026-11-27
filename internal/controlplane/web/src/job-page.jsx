import React, { Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowLeft, ArrowUpRight, Ban, Check, ChevronRight, CircleAlert, CircleDashed, Download, LoaderCircle, Minus, RotateCcw, ShieldAlert, ShieldCheck } from "lucide-react";
import { api, SignedOutError } from "./api.js";
import { AgentBadges } from "./agent-badge.jsx";
import { harnessList, labelList, waitingForRunner } from "./agents.js";
import { State, relativeTime, useNow } from "./display.jsx";
import { failureReason, formatDuration, isFinished, jobState, linkParts, mergeGate, noPullRequest, prLink, prState, sourceLink, sourceLinkText } from "./jobs.js";
import { RiskBadge } from "./risk-badge.jsx";
import { Source } from "./source-mark.jsx";
import { latestAction, noStepsNote, pipeline, pollEvents, runSpend, spend, spendTitle, stepTime, steps } from "./step-log.js";
import { Button } from "@/components/ui/button";

const pollMillis = 2000;
const eventPage = 128;

// useRunEvents reads every event of a run, and keeps reading while it can send more.
// PullRequest links the job's pull request and says whether it is open,
// merged or closed, and since when.
function PullRequest({ ticket, link }) {
  const state = prState(ticket);
  return <span className="job-pr-value">
    <a href={link} target="_blank" rel="noreferrer">#{ticket.pr_number}</a>
    {state && <> · {state}{ticket.pr_state_at && <> <time dateTime={ticket.pr_state_at}>{relativeTime(ticket.pr_state_at)}</time></>}</>}
  </span>;
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
  held: [CircleAlert, "text-warning"],
  running: [LoaderCircle, "text-info state-working"],
  failed: [CircleAlert, "text-danger"],
  timed_out: [CircleAlert, "text-danger"],
  cancelled: [Minus, "text-muted-foreground"],
  skipped: [Minus, "text-faint"],
  pending: [CircleDashed, "text-faint"],
  unknown: [Minus, "text-faint"],
};

const stateWords = { succeeded: "Passed", held: "Held", running: "Running", failed: "Failed", timed_out: "Timed out", cancelled: "Cancelled", skipped: "Skipped", pending: "Waiting", unknown: "" };

// Pipeline draws the preset's steps left to right, joined in the order they
// run. Each node shows how far the latest attempt got. Choosing one opens its
// output below.
function Pipeline({ nodes, onChoose, now = Date.now() }) {
  return <section className="pipeline" aria-label="Pipeline">
    <ol>{nodes.map((step, index) => {
      const [Icon, tone] = stepIcons[step.state] || stepIcons.unknown;
      const empty = !step.output && !step.error;
      const meta = step.state === "held" ? stateWords.held : step.state !== "skipped" && stepTime(step, now) || stateWords[step.state];
      return <li key={step.name} data-state={step.state}>
        {index > 0 && <span className="pipeline-edge" aria-hidden="true" />}
        <button type="button" className="pipeline-node" disabled={empty} onClick={() => onChoose(step.name)} aria-label={`${step.name}: ${stateWords[step.state] || step.state}`}>
          <Icon className={`size-4 shrink-0 ${tone}`} aria-hidden="true" />
          <span className="pipeline-name">{step.name}</span>
          <span className="pipeline-meta">{meta}</span>
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
      <span className="step-time">{step.state === "skipped" ? "Skipped" : step.state === "held" ? "Held" : stepTime(step, now)}</span>
      <ChevronRight className="step-chevron size-4" aria-hidden="true" />
    </button>
    {open && !empty && <StepOutput text={step.output || step.error} />}
  </li>;
}

// When the merge gate holds a pull request for human review (needs_you), the
// merge step exited 0 by leaving the PR open rather than merging it. Marking
// that step as held avoids showing a misleading green checkmark on merge while
// keeping auto-merging PRs waiting on CI (waiting_for_ci) unheld.
function markHeldMerge(list, held) {
  if (!held) return list;
  return list.map(step => step.name === "merge" && step.state === "succeeded" ? { ...step, state: "held" } : step);
}

function Run({ run, number, presetSteps, waitingFor, waitingForLabels = [], held = false }) {
  const { events, error, loaded } = useRunEvents(run);
  const list = markHeldMerge(steps(events, run.stages || [], run.status), held);
  const now = useNow(run.status === "running");
  const current = run.status === "running" ? list.find(step => step.state === "running") : null;
  const action = current ? latestAction(current.output) : "";
  const nodes = presetSteps ? markHeldMerge(pipeline(presetSteps, list, run.status), held) : [];
  // chosen counts each choice, so choosing the same step twice still opens it.
  const [chosen, setChosen] = useState({ name: "", times: 0 });
  const choose = name => setChosen(previous => ({ name, times: previous.times + 1 }));
  return <section className="run" aria-label={`Attempt ${number}`}>
    <div className="run-head">
      <h2>Attempt {number}</h2>
      <span>{run.status === "queued" ? waitingForRunner(waitingFor, waitingForLabels) : `${run.status.replaceAll("_", " ")} on ${run.runner || "a runner"}`}</span>
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

// CancelJob stops an active (queued or running) job.
function CancelJob({ ticketID, onDone }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function cancel() {
    setBusy(true);
    setError("");
    try {
      await api(`/api/v1/tickets/${encodeURIComponent(ticketID)}/cancel`, { method: "POST" });
      await onDone?.();
    } catch (failure) {
      setError(failure.message);
    } finally {
      setBusy(false);
    }
  }
  return <div className="cancel-job">
    <Button variant="outline" size="sm" className="text-danger" onClick={cancel} disabled={busy}><Ban className="size-3.5" aria-hidden="true" />{busy ? "Cancelling" : "Cancel"}</Button>
    {error && <p className="form-error" role="alert">{error}</p>}
  </div>;
}

// MergeGatePanel shows the merge gate's decision, risk badge, and explanation
// when the ticket carries a persisted merge-gate verdict.
function MergeGatePanel({ ticket }) {
  const gate = mergeGate(ticket);
  if (!gate) return null;
  const approved = gate.decision?.value === "approve";
  const DecisionIcon = approved ? ShieldCheck : ShieldAlert;
  return <section className="panel merge-gate" data-decision={gate.decision?.value || undefined} data-risk={gate.risk?.level || undefined} aria-labelledby="job-merge-gate">
    <div className="merge-gate-head">
      <h2 id="job-merge-gate" className="label">Merge gate</h2>
      <div className="merge-gate-verdict">
        {gate.decision && <span className={`merge-gate-decision${approved ? " merge-gate-decision-approved" : " merge-gate-decision-review"}`}>
          <DecisionIcon className="size-3.5" aria-hidden="true" />
          <span>{gate.decision.label}</span>
        </span>}
        <RiskBadge ticket={ticket} />
      </div>
    </div>
    {gate.reason && <p className="prose merge-gate-reason"><Linked text={gate.reason} /></p>}
  </section>;
}

export function JobView({ detail, role = "admin", onChange }) {
  const canRun = role !== "viewer";
  const { ticket, entries = [], runs = [], steps: presetSteps = [], waiting_for: waitingFor = [], waiting_for_labels: waitingForLabels = [], runs_on: detailRunsOn = [] } = detail;
  const runsOn = detailRunsOn.length ? detailRunsOn : (ticket.runs_on || []);
  const state = jobState(ticket, runs.some(run => run.status === "running"));
  const pr = prLink(ticket);
  const original = sourceLink(ticket);
  const prompt = entries.find(entry => entry.kind === "request")?.body || "";
  const result = [...runs].reverse().find(run => run.report?.summary)?.report.summary || "";
  const failure = state === "failed" ? failureReason(entries) : "";
  const attempts = runs.map((run, index) => ({ run, number: index + 1 })).reverse();
  const waitingWarning = waitingFor.length > 0 && waitingForLabels.length > 0
    ? `No online runner has ${harnessList(waitingFor)} and ${labelList(waitingForLabels)}, which this job requires, so it cannot start. Configure a runner with them and restart the runner.`
    : waitingFor.length > 0
      ? `No online runner has ${harnessList(waitingFor)}, which this job's agents run, so it cannot start. Install ${waitingFor.length === 1 ? "it" : "them"} on a runner and restart the runner.`
      : waitingForLabels.length > 0
        ? `No online runner has ${labelList(waitingForLabels)}, which this job targets, so it cannot start. Add ${waitingForLabels.length === 1 ? "it" : "them"} to a runner in runner.yaml and restart the runner.`
        : "";
  return <div className="page job-page">
    <a className="back-link" href="#/jobs"><ArrowLeft className="size-3.5" aria-hidden="true" />Jobs</a>
    <header className="job-head">
      <h1>{ticket.title || ticket.id}</h1>
      <div className="job-actions">
        <State value={state} />
        {canRun && (isFinished(state) ? <RunAgain ticketID={ticket.id} onDone={onChange} /> : <CancelJob ticketID={ticket.id} onDone={onChange} />)}
      </div>
    </header>
    <dl className="job-facts">
      <div><dt>Source</dt><dd className="job-source"><Source source={ticket.source} />{original && <a href={original} target="_blank" rel="noreferrer">{sourceLinkText(original)}<ArrowUpRight className="size-3.5" aria-hidden="true" /></a>}</dd></div>
      <div><dt>Repository</dt><dd>{ticket.repo}</dd></div>
      <div><dt>Preset</dt><dd>{ticket.agent}</dd></div>
      {runsOn.length > 0 && <div><dt>Runs on</dt><dd><span className="runner-labels">{runsOn.map(label => <span key={label} className="runner-label">{label}</span>)}</span></dd></div>}
      <div><dt>Pull request</dt><dd>{pr ? <PullRequest ticket={ticket} link={pr} /> : noPullRequest(state)}</dd></div>
      <div><dt>Started by</dt><dd>{ticket.created_by}</dd></div>
      <div><dt>Created</dt><dd><time dateTime={ticket.created_at}>{relativeTime(ticket.created_at)}</time></dd></div>
    </dl>
    {waitingWarning && <p className="page-warning" role="status">{waitingWarning}</p>}
    {failure && <section className="panel job-failure" aria-labelledby="job-failure">
      <h2 id="job-failure" className="label"><CircleAlert className="size-3.5" aria-hidden="true" />Why it failed</h2>
      <p className="prose"><Linked text={failure} /></p>
    </section>}
    <MergeGatePanel ticket={ticket} />
    {result && !failure && isFinished(state) && <section className="panel" aria-labelledby="job-result"><h2 id="job-result" className="label">Result</h2><p className="prose"><Linked text={result} /></p></section>}
    {prompt && <details className="panel prompt"><summary className="label">Prompt</summary><p className="prose">{prompt}</p></details>}
    {attempts.length ? attempts.map(({ run, number }, index) => <Run key={run.id} run={run} number={number} presetSteps={index === 0 ? presetSteps : null} waitingFor={index === 0 ? waitingFor : []} waitingForLabels={index === 0 ? waitingForLabels : []} held={index === 0 && state === "needs_you"} />) : <>
      {presetSteps.length > 0 && <Pipeline nodes={pipeline(presetSteps, [], "")} onChoose={() => {}} />}
      <p className="page-note">Waiting for a runner.</p>
    </>}
    <History entries={entries} />
  </div>;
}

// pollJob says whether a job can still change on its own, so JobPage should
// keep polling its detail. A job in done, failed or cancelled with no active
// run and no open or shipping pull request has reached a terminal state.
export function pollJob(detail) {
  if (!detail?.ticket) return true;
  const { ticket, runs = [] } = detail;
  if (!["done", "failed", "cancelled"].includes(ticket.state)) return true;
  if (runs.some(run => pollEvents(run))) return true;
  if (ticket.pr_state === "open" || Boolean(ticket.shipping)) return true;
  return false;
}

// The app keys this by job, so a reply for a job someone has left never shows under another.
export function JobPage({ jobID, role = "admin", onSignedOut }) {
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState("");
  const loop = useRef(null);

  useEffect(() => {
    let stopped = false;
    let timer;
    let requestID = 0;
    let controller;

    const load = async () => {
      window.clearTimeout(timer);
      timer = undefined;
      controller?.abort();
      const currentController = new AbortController();
      controller = currentController;
      const currentID = ++requestID;
      let keepPolling = false;
      try {
        const next = await api(`/api/v1/tickets/${encodeURIComponent(jobID)}`, { signal: currentController.signal });
        if (stopped || currentID !== requestID) return;
        setDetail(next);
        setError("");
        keepPolling = pollJob(next);
      } catch (failure) {
        if (stopped || currentID !== requestID || failure?.name === "AbortError") return;
        if (failure instanceof SignedOutError) {
          onSignedOut?.();
          return;
        }
        setError(failure.message);
        keepPolling = true;
      }
      if (!stopped && currentID === requestID && keepPolling) {
        timer = window.setTimeout(load, pollMillis);
      }
    };

    loop.current = load;
    load();
    return () => {
      stopped = true;
      loop.current = null;
      window.clearTimeout(timer);
      controller?.abort();
    };
  }, [jobID, onSignedOut]);

  const refresh = useCallback(() => loop.current?.(), []);
  if (!detail) return <div className="page">
    <a className="back-link" href="#/jobs"><ArrowLeft className="size-3.5" aria-hidden="true" />Jobs</a>
    {error ? <p className="form-error" role="alert">{error}</p> : <p className="page-note" role="status">Loading job</p>}
  </div>;
  return <>{error && <p className="form-error page-banner" role="alert">{error}</p>}<JobView detail={detail} role={role} onChange={refresh} /></>;
}
