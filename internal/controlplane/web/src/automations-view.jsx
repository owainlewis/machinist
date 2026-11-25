import React, { useEffect, useRef, useState } from "react";
import { CalendarClock, Play, RotateCcw, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import {
  automationExample, automationRunPath, canRunAutomation, formatDueAt, formatNextDue,
  isPromptEdited, runAutomationBody,
} from "./automations.js";
import { State, relativeTime } from "./display.jsx";
import { jobState } from "./jobs.js";
import { jobHref } from "./routes.js";
import { createStatusLoader } from "./status-loader.js";

const pollMillis = 2000;

function AutomationRow({ automation, running, now, onRun }) {
  const last = automation.last_ticket;
  const lastState = last ? jobState(last, running.has(last.id)) : "";
  const dueAt = formatDueAt(automation.next_due, automation.timezone);
  return <tr data-automation={automation.name}>
    <td className="automation-name"><strong>{automation.name}</strong></td>
    <td className="automation-detail">{automation.repo}</td>
    <td className="automation-detail automation-preset">{automation.agent}</td>
    <td className="automation-schedule">
      <code>{automation.schedule}</code>
      {automation.timezone && <span className="automation-tz">{automation.timezone}</span>}
    </td>
    <td className="automation-due">
      <time dateTime={automation.next_due} title={dueAt || undefined}>{formatNextDue(automation.next_due, now)}</time>
      {dueAt && <span className="automation-due-at">{dueAt}</span>}
    </td>
    <td className="automation-last">
      {last
        ? <div className="automation-last-cell">
          <a className="automation-last-run" href={jobHref(last.id)} title={last.outcome || undefined}>
            <State value={lastState} />
            <time dateTime={last.updated_at || last.created_at}>{relativeTime(last.updated_at || last.created_at, now)}</time>
          </a>
          {last.outcome && <span className="automation-outcome" title={last.outcome}>{last.outcome}</span>}
        </div>
        : <span className="automation-never">Never</span>}
    </td>
    <td className="automation-action">
      <Button type="button" variant="outline" size="sm" onClick={() => onRun(automation)} aria-label={`Run ${automation.name}`}>
        <Play className="size-3.5" aria-hidden="true" />
        Run
      </Button>
    </td>
  </tr>;
}

function AutomationsEmpty() {
  return <section className="panel automations-empty" aria-labelledby="automations-empty-title">
    <div className="automations-empty-head">
      <CalendarClock className="size-5" aria-hidden="true" />
      <h2 id="automations-empty-title" className="chart-title">No automations configured</h2>
    </div>
    <p className="page-note">
      Add a Markdown file under <code>factory/automations/</code> with YAML front matter (<code>schedule</code>, <code>timezone</code>, <code>repo</code>, <code>preset</code>) and a prompt body, then restart the control plane.
    </p>
    <div className="automations-example-block">
      <span className="inspector-file-label"><code>factory/automations/daily-review.md</code></span>
      <pre className="inspector-code automations-example">{automationExample}</pre>
    </div>
    <p className="ticket-meta">
      Quote <code>schedule</code> because cron expressions can start with <code>*</code>. <code>repo</code> must be connected and <code>preset</code> must name a preset in <code>factory/presets/</code>.
    </p>
  </section>;
}

function RunAutomationDialog({ automation, running, onClose, onSignedOut }) {
  const [prompt, setPrompt] = useState(automation.prompt || "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const textareaRef = useRef(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    mounted.current = true;
    textareaRef.current?.focus();
    return () => { mounted.current = false; };
  }, []);

  useEffect(() => {
    const onKeyDown = event => {
      if (event.key === "Escape" && !pending) onCloseRef.current?.();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [pending]);

  const edited = isPromptEdited(prompt, automation.prompt);
  const ready = canRunAutomation(prompt) && !pending;
  const activeTicket = automation.last_ticket?.state === "working" ? automation.last_ticket : null;
  const activeState = activeTicket ? jobState(activeTicket, running.has(activeTicket.id)) : "";

  async function submit(event) {
    event.preventDefault();
    if (!ready) return;
    setPending(true);
    setError("");
    try {
      const body = runAutomationBody(prompt, automation.prompt);
      const ticket = await api(automationRunPath(automation.name), { method: "POST", body });
      if (!mounted.current) return;
      onClose();
      window.location.hash = jobHref(ticket.id);
    } catch (failure) {
      if (!mounted.current) return;
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
      setPending(false);
    }
  }

  function submitOnShortcut(event) {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) submit(event);
  }

  return <div className="modal-backdrop" onClick={event => { if (event.target === event.currentTarget && !pending) onClose(); }}>
    <div className="modal" role="dialog" aria-modal="true" aria-labelledby="automation-run-title">
      <header className="modal-head">
        <div>
          <h2 id="automation-run-title">Run {automation.name}</h2>
          <p className="modal-meta">
            <span>{automation.repo}</span>
            <span aria-hidden="true">·</span>
            <span>{automation.agent}</span>
            <span aria-hidden="true">·</span>
            <code>{automation.schedule}</code>
            {automation.timezone && <span>{automation.timezone}</span>}
          </p>
        </div>
        <button type="button" className="icon-button" onClick={onClose} disabled={pending} aria-label="Close">
          <X className="size-4" aria-hidden="true" />
        </button>
      </header>
      <form className="modal-body" onSubmit={submit}>
        {activeTicket && <p className="page-warning" role="status">
          This automation already has an active run (<a href={jobHref(activeTicket.id)}>{activeState === "running" ? "running" : "queued"}</a>). Starting now will open it instead of queuing a duplicate.
        </p>}
        <div>
          <div className="automation-prompt-head">
            <label className="field-label" htmlFor="automation-run-prompt">Prompt</label>
            {edited && <div className="automation-prompt-edited">
              <span className="pill pill-info">Edited for this run</span>
              <button type="button" className="link-button automation-reset" onClick={() => setPrompt(automation.prompt || "")} disabled={pending}>
                <RotateCcw className="size-3" aria-hidden="true" />
                Reset to saved
              </button>
            </div>}
          </div>
          <textarea
            ref={textareaRef}
            id="automation-run-prompt"
            className="field-control automation-prompt-input"
            rows={9}
            value={prompt}
            onChange={event => setPrompt(event.target.value)}
            onKeyDown={submitOnShortcut}
            disabled={pending}
          />
          <p className="field-hint">
            {edited
              ? <>Changes apply to this run only. <code>factory/automations/{automation.name}.md</code> is not modified.</>
              : <>Run with the saved prompt or edit it for this run only. <code>factory/automations/{automation.name}.md</code> is never modified.</>}
          </p>
        </div>
        {error && <p className="form-error" role="alert">{error}</p>}
        <footer className="modal-foot">
          <span className="field-hint new-job-shortcut">Ctrl or ⌘ + Enter</span>
          <div className="modal-foot-actions">
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>Cancel</Button>
            <Button type="submit" disabled={!ready}>
              <Play className="size-4" aria-hidden="true" />
              {pending ? "Starting" : "Run now"}
            </Button>
          </div>
        </footer>
      </form>
    </div>
  </div>;
}

export function AutomationsView({ running = new Set(), onSignedOut }) {
  const [automations, setAutomations] = useState([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState(null);
  const [now, setNow] = useState(() => Date.now());
  const onSignedOutRef = useRef(onSignedOut);
  onSignedOutRef.current = onSignedOut;

  useEffect(() => {
    let stopped = false;
    let timer;
    const loader = createStatusLoader({
      request: () => api("/api/v1/automations"),
      apply: result => {
        if (result.kind === "success") {
          const list = Array.isArray(result.status?.automations) ? result.status.automations : [];
          setAutomations(list);
          setNow(Date.now());
          setError("");
          setLoaded(true);
        } else if (result.message === new SignedOutError().message) {
          onSignedOutRef.current?.();
        } else {
          setError(result.message);
        }
      },
    });
    const load = async () => {
      await loader.refresh();
      if (!stopped) timer = window.setTimeout(load, pollMillis);
    };
    load();
    return () => { stopped = true; loader.cancel(); window.clearTimeout(timer); };
  }, []);

  // Keep the selected automation's metadata fresh (e.g. last_ticket) without
  // overwriting the user's in-progress prompt edits inside the dialog.
  const selectedLive = selected
    ? automations.find(item => item.name === selected.name) || selected
    : null;

  return <>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading automations</p>}
    {loaded && !error && automations.length === 0 && <AutomationsEmpty />}
    {loaded && automations.length > 0 && <div className="list-card table-scroll">
      <table className="automations">
        <thead>
          <tr>
            <th scope="col">Automation</th>
            <th scope="col">Repository</th>
            <th scope="col" className="automation-preset-col">Preset</th>
            <th scope="col">Schedule</th>
            <th scope="col">Next due</th>
            <th scope="col">Last run</th>
            <th scope="col" className="automation-action-col"><span className="sr-only">Action</span></th>
          </tr>
        </thead>
        <tbody>
          {automations.map(automation => <AutomationRow
            key={automation.name}
            automation={automation}
            running={running}
            now={now}
            onRun={setSelected}
          />)}
        </tbody>
      </table>
    </div>}
    {selectedLive && <RunAutomationDialog
      key={selectedLive.name}
      automation={selectedLive}
      running={running}
      onClose={() => setSelected(null)}
      onSignedOut={onSignedOut}
    />}
  </>;
}
