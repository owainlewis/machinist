import React, { useCallback, useEffect, useRef, useState } from "react";
import { CalendarClock, Pencil, Play, Plus, RotateCcw, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { harnessLabel } from "./agents.js";
import { api, SignedOutError } from "./api.js";
import {
  automationExample, automationHarnesses, automationPath, automationPayload, automationRunPath,
  canRunAutomation, canSaveAutomation, formatDueAt, formatNextDue, isRunEdited, onlineHarnessSet,
  runAutomationBody,
} from "./automations.js";
import { State, relativeTime } from "./display.jsx";
import { HarnessMark } from "./harness-mark.jsx";
import { jobState, runningJobs } from "./jobs.js";
import { initialInputValues, presetInputs } from "./new-job.js";
import { jobHref } from "./routes.js";
import { createStatusLoader } from "./status-loader.js";

const pollMillis = 2000;

function harnessOptionLabel(name, onlineHarnesses) {
  const label = harnessLabel(name);
  if (onlineHarnesses.size > 0 && !onlineHarnesses.has(name)) return `${label} (no online runner)`;
  return label;
}

function AutomationRow({ automation, running, now, canRun, confirmingDelete, deleting, onRun, onEdit, onAskDelete, onCancelDelete, onConfirmDelete }) {
  const last = automation.last_ticket;
  const lastState = last ? jobState(last, running.has(last.id)) : "";
  const scheduled = Boolean(automation.schedule);
  const dueAt = scheduled ? formatDueAt(automation.next_due, automation.timezone) : "";
  const hasAgentOverride = Boolean(automation.harness || automation.model);

  return <tr data-automation={automation.name}>
    <td className="automation-name">
      <div className="automation-name-cell">
        <strong>{automation.name}</strong>
        {!automation.editable && <span className="automation-source-tag" title="Defined in factory/automations/">File</span>}
      </div>
    </td>
    <td className="automation-detail">{automation.repo}</td>
    <td className="automation-detail automation-preset">
      <div className="automation-preset-cell">
        <span>{automation.agent}</span>
        {hasAgentOverride && <span className="automation-override-badge">
          {automation.harness && <HarnessMark harness={automation.harness} className="automation-override-mark" />}
          <span>{[automation.harness ? harnessLabel(automation.harness) : "", automation.model].filter(Boolean).join(" · ")}</span>
        </span>}
      </div>
    </td>
    <td className="automation-schedule">
      {scheduled
        ? <><code>{automation.schedule}</code>{automation.timezone && <span className="automation-tz">{automation.timezone}</span>}</>
        : <span className="automation-manual">Manual</span>}
    </td>
    <td className="automation-due">
      {scheduled
        ? <><time dateTime={automation.next_due} title={dueAt || undefined}>{formatNextDue(automation.next_due, now)}</time>{dueAt && <span className="automation-due-at">{dueAt}</span>}</>
        : <span className="automation-never">—</span>}
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
      {canRun && <div className="automation-actions">
        {confirmingDelete
          ? <>
            <Button type="button" variant="destructive" size="sm" disabled={deleting} onClick={() => onConfirmDelete(automation)}>
              {deleting ? "Deleting" : "Delete"}
            </Button>
            <Button type="button" variant="outline" size="sm" disabled={deleting} onClick={onCancelDelete}>Cancel</Button>
          </>
          : <>
            <Button type="button" variant="outline" size="sm" onClick={() => onRun(automation)} aria-label={`Run ${automation.name}`}>
              <Play className="size-3.5" aria-hidden="true" />
              Run
            </Button>
            {automation.editable && <>
              <button type="button" className="icon-button" onClick={() => onEdit(automation)} aria-label={`Edit ${automation.name}`} title="Edit automation">
                <Pencil className="size-3.5" aria-hidden="true" />
              </button>
              <button type="button" className="icon-button automation-delete-button" onClick={() => onAskDelete(automation.name)} aria-label={`Delete ${automation.name}`} title="Delete automation">
                <Trash2 className="size-3.5" aria-hidden="true" />
              </button>
            </>}
          </>}
      </div>}
    </td>
  </tr>;
}

function AutomationsEmpty({ canRun, onNew }) {
  return <section className="panel automations-empty" aria-labelledby="automations-empty-title">
    <div className="automations-empty-head">
      <CalendarClock className="size-5" aria-hidden="true" />
      <h2 id="automations-empty-title" className="chart-title">No automations yet</h2>
    </div>
    <p className="page-note">
      Save reusable agent prompts with a repository, preset, optional harness/model, and optional cron schedule — then run them on demand or on a schedule. Every run appears in <a href="#/jobs">Jobs</a>.
    </p>
    {canRun && <div className="automations-empty-actions">
      <Button type="button" onClick={onNew}>
        <Plus className="size-4" aria-hidden="true" />
        New automation
      </Button>
    </div>}
    <div className="automations-example-block">
      <span className="inspector-file-label">You can also check in files under <code>factory/automations/</code>:</span>
      <pre className="inspector-code automations-example">{automationExample}</pre>
    </div>
  </section>;
}

function useEditorOptions(onSignedOut) {
  const [options, setOptions] = useState(null);
  const [loadError, setLoadError] = useState("");
  const onSignedOutRef = useRef(onSignedOut);
  onSignedOutRef.current = onSignedOut;

  useEffect(() => {
    let stopped = false;
    Promise.all([api("/api/v1/repositories"), api("/api/v1/presets")]).then(([repos, presets]) => {
      if (stopped) return;
      setOptions({
        repositories: Array.isArray(repos?.repositories) ? repos.repositories : [],
        presets: Array.isArray(presets?.presets) ? presets.presets : [],
        defaultPreset: presets?.default || "",
      });
      setLoadError("");
    }).catch(failure => {
      if (stopped) return;
      if (failure instanceof SignedOutError) onSignedOutRef.current?.();
      else setLoadError(failure.message);
    });
    return () => { stopped = true; };
  }, []);

  return { options, loadError };
}

function AutomationEditorDialog({ automation, onlineHarnesses, onClose, onSaved, onSignedOut }) {
  const editing = Boolean(automation);
  const { options, loadError } = useEditorOptions(onSignedOut);
  const [name, setName] = useState(automation?.name || "");
  const [repo, setRepo] = useState(automation?.repo || "");
  const [preset, setPreset] = useState(automation?.agent || "");
  const [harness, setHarness] = useState(automation?.harness || "");
  const [model, setModel] = useState(automation?.model || "");
  const [schedule, setSchedule] = useState(automation?.schedule || "");
  const [timezone, setTimezone] = useState(automation?.timezone || "UTC");
  const [vars, setVars] = useState(() => (automation?.vars ? { ...automation.vars } : {}));
  const [prompt, setPrompt] = useState(automation?.prompt || "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const nameRef = useRef(null);
  const promptRef = useRef(null);
  const onCloseRef = useRef(onClose);
  const varsInitialized = useRef(false);
  onCloseRef.current = onClose;

  useEffect(() => {
    mounted.current = true;
    (editing ? promptRef : nameRef).current?.focus();
    return () => { mounted.current = false; };
  }, [editing]);

  useEffect(() => {
    if (!options) return;
    const nextPreset = preset || options.defaultPreset || options.presets[0]?.name || "";
    setRepo(current => current || options.repositories[0] || "");
    setPreset(current => current || nextPreset);
    if (!varsInitialized.current && nextPreset) {
      varsInitialized.current = true;
      const presetObj = options.presets.find(item => item.name === nextPreset);
      setVars(initialInputValues(presetObj, editing && nextPreset === automation?.agent ? automation?.vars : null));
    }
  }, [options, preset, editing, automation]);

  useEffect(() => {
    const onKeyDown = event => {
      if (event.key === "Escape" && !pending) onCloseRef.current?.();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [pending]);

  function changeHarness(next) {
    setHarness(next);
    if (next !== harness) setModel("");
  }

  function changePreset(next) {
    setPreset(next);
    const nextObj = options?.presets.find(item => item.name === next);
    setVars(initialInputValues(nextObj, editing && next === automation?.agent ? automation?.vars : null));
  }

  const selectedPreset = options?.presets.find(item => item.name === preset);
  const declaredInputs = presetInputs(selectedPreset);
  const form = { name, repo, preset, harness, model, schedule, timezone, prompt, runs_on: automation?.runs_on, vars };
  const ready = canSaveAutomation(form, { requireName: !editing, presetInputs: declaredInputs }) && !pending;
  const repoOptions = options ? (repo && !options.repositories.includes(repo) ? [repo, ...options.repositories] : options.repositories) : (repo ? [repo] : []);
  const presetNames = options ? options.presets.map(item => item.name) : [];
  const presetOptions = options ? (preset && !presetNames.includes(preset) ? [preset, ...presetNames] : presetNames) : (preset ? [preset] : []);

  async function submit(event) {
    event.preventDefault();
    if (!ready) return;
    setPending(true);
    setError("");
    try {
      const path = editing ? automationPath(automation.name) : "/api/v1/automations";
      const method = editing ? "PUT" : "POST";
      await api(path, { method, body: automationPayload(form, { includeName: !editing, presetInputs: declaredInputs }) });
      if (!mounted.current) return;
      onSaved?.();
      onClose();
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
    <div className="modal" role="dialog" aria-modal="true" aria-labelledby="automation-editor-title">
      <header className="modal-head">
        <div>
          <h2 id="automation-editor-title">{editing ? `Edit ${automation.name}` : "New automation"}</h2>
          <p className="modal-meta">
            <span>{editing ? "Changes apply to future runs." : "Save a reusable job template you can run manually or on a schedule."}</span>
          </p>
        </div>
        <button type="button" className="icon-button" onClick={onClose} disabled={pending} aria-label="Close">
          <X className="size-4" aria-hidden="true" />
        </button>
      </header>
      <form className="modal-body" onSubmit={submit}>
        {loadError && <p className="form-error" role="alert">{loadError}</p>}
        {!editing && <div>
          <label className="field-label" htmlFor="automation-edit-name">Name</label>
          <input
            ref={nameRef}
            id="automation-edit-name"
            className="field-control"
            value={name}
            onChange={event => setName(event.target.value)}
            placeholder="nightly-review"
            disabled={pending}
          />
          <p className="field-hint">Letters, digits, hyphens, and underscores.</p>
        </div>}
        <div className="automation-fields-row">
          <div>
            <label className="field-label" htmlFor="automation-edit-repo">Repository</label>
            <Select id="automation-edit-repo" value={repo} onChange={setRepo} disabled={pending || !repoOptions.length}>
              {repoOptions.map(item => <option key={item} value={item}>{item}</option>)}
            </Select>
          </div>
          <div>
            <label className="field-label" htmlFor="automation-edit-preset">Preset</label>
            <Select id="automation-edit-preset" value={preset} onChange={changePreset} disabled={pending || !presetOptions.length}>
              {presetOptions.map(item => <option key={item} value={item}>{item}</option>)}
            </Select>
          </div>
        </div>
        {declaredInputs.length > 0 && <fieldset className="preset-inputs">
          <legend className="field-label">Inputs</legend>
          <div className="preset-inputs-grid">
            {declaredInputs.map(item => <div key={item.name} className="preset-input-field">
              <label className="field-label" htmlFor={`automation-edit-var-${item.name}`}>
                <code>{item.name}</code>
                {item.required ? <span className="field-required" aria-label="required">*</span> : <span className="field-optional">(optional)</span>}
              </label>
              <input
                id={`automation-edit-var-${item.name}`}
                className="field-control"
                value={vars[item.name] ?? ""}
                onChange={event => setVars(current => ({ ...current, [item.name]: event.target.value }))}
                onKeyDown={submitOnShortcut}
                placeholder={item.default !== undefined ? item.default : ""}
                required={item.required}
                disabled={pending}
              />
              {item.description && <p className="field-hint">{item.description}</p>}
            </div>)}
          </div>
        </fieldset>}
        <div className="automation-fields-row">
          <div>
            <label className="field-label" htmlFor="automation-edit-harness">Harness <span className="field-optional">(optional)</span></label>
            <Select id="automation-edit-harness" value={harness} onChange={changeHarness} disabled={pending}>
              <option value="">Preset default</option>
              {automationHarnesses.map(item => <option key={item} value={item}>{harnessOptionLabel(item, onlineHarnesses)}</option>)}
            </Select>
          </div>
          <div>
            <label className="field-label" htmlFor="automation-edit-model">Model <span className="field-optional">(optional)</span></label>
            <input
              id="automation-edit-model"
              className="field-control"
              value={model}
              onChange={event => setModel(event.target.value)}
              placeholder="Harness default"
              disabled={pending}
            />
          </div>
        </div>
        <div className="automation-fields-row">
          <div>
            <label className="field-label" htmlFor="automation-edit-schedule">Schedule <span className="field-optional">(optional cron)</span></label>
            <input
              id="automation-edit-schedule"
              className="field-control"
              value={schedule}
              onChange={event => setSchedule(event.target.value)}
              placeholder="Leave blank for manual, or 0 9 * * 1-5"
              disabled={pending}
            />
          </div>
          <div>
            <label className="field-label" htmlFor="automation-edit-timezone">Timezone</label>
            <input
              id="automation-edit-timezone"
              className="field-control"
              value={timezone}
              onChange={event => setTimezone(event.target.value)}
              placeholder="UTC"
              disabled={pending || !schedule.trim()}
            />
          </div>
        </div>
        <div>
          <label className="field-label" htmlFor="automation-edit-prompt">Prompt</label>
          <textarea
            ref={promptRef}
            id="automation-edit-prompt"
            className="field-control automation-prompt-input"
            rows={7}
            value={prompt}
            onChange={event => setPrompt(event.target.value)}
            onKeyDown={submitOnShortcut}
            placeholder="Describe what the agent should do when this automation runs."
            disabled={pending}
          />
        </div>
        {error && <p className="form-error" role="alert">{error}</p>}
        <footer className="modal-foot">
          <span className="field-hint new-job-shortcut">Ctrl or ⌘ + Enter</span>
          <div className="modal-foot-actions">
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>Cancel</Button>
            <Button type="submit" disabled={!ready}>{pending ? "Saving" : (editing ? "Save changes" : "Create automation")}</Button>
          </div>
        </footer>
      </form>
    </div>
  </div>;
}

function RunAutomationDialog({ automation, running, onlineHarnesses, onClose, onSignedOut }) {
  const { options } = useEditorOptions(onSignedOut);
  const [repo, setRepo] = useState(automation.repo || "");
  const [preset, setPreset] = useState(automation.agent || "");
  const [harness, setHarness] = useState(automation.harness || "");
  const [model, setModel] = useState(automation.model || "");
  const [vars, setVars] = useState(() => (automation.vars ? { ...automation.vars } : {}));
  const [prompt, setPrompt] = useState(automation.prompt || "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const textareaRef = useRef(null);
  const onCloseRef = useRef(onClose);
  const varsInitialized = useRef(false);
  onCloseRef.current = onClose;

  useEffect(() => {
    mounted.current = true;
    textareaRef.current?.focus();
    return () => { mounted.current = false; };
  }, []);

  useEffect(() => {
    if (!options || varsInitialized.current) return;
    varsInitialized.current = true;
    const presetObj = options.presets.find(item => item.name === preset);
    setVars(initialInputValues(presetObj, automation.vars));
  }, [options, preset, automation]);

  useEffect(() => {
    const onKeyDown = event => {
      if (event.key === "Escape" && !pending) onCloseRef.current?.();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [pending]);

  function changeHarness(next) {
    setHarness(next);
    if (next !== harness) setModel("");
  }

  function changePreset(next) {
    setPreset(next);
    const nextObj = options?.presets.find(item => item.name === next);
    setVars(initialInputValues(nextObj, next === automation.agent ? automation.vars : null));
  }

  function resetToSaved() {
    setRepo(automation.repo || "");
    setPreset(automation.agent || "");
    setHarness(automation.harness || "");
    setModel(automation.model || "");
    const savedPresetObj = options?.presets.find(item => item.name === (automation.agent || ""));
    setVars(initialInputValues(savedPresetObj, automation.vars));
    setPrompt(automation.prompt || "");
  }

  const selectedPreset = options?.presets.find(item => item.name === preset);
  const declaredInputs = presetInputs(selectedPreset);
  const current = declaredInputs.length > 0 ? { repo, preset, harness, model, prompt, vars } : { repo, preset, harness, model, prompt };
  const edited = isRunEdited(current, automation, declaredInputs);
  const ready = canRunAutomation(prompt, vars, declaredInputs) && Boolean(repo && preset) && !pending;
  const activeTicket = automation.last_ticket?.state === "working" ? automation.last_ticket : null;
  const activeState = activeTicket ? jobState(activeTicket, running.has(activeTicket.id)) : "";
  const repoOptions = options ? (repo && !options.repositories.includes(repo) ? [repo, ...options.repositories] : options.repositories) : [repo].filter(Boolean);
  const presetNames = options ? options.presets.map(item => item.name) : [];
  const presetOptions = options ? (preset && !presetNames.includes(preset) ? [preset, ...presetNames] : presetNames) : [preset].filter(Boolean);

  async function submit(event) {
    event.preventDefault();
    if (!ready) return;
    setPending(true);
    setError("");
    try {
      const body = runAutomationBody(current, automation, declaredInputs);
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
            <span>{automation.schedule ? <><code>{automation.schedule}</code>{automation.timezone ? ` ${automation.timezone}` : ""}</> : "Manual automation"}</span>
            <span aria-hidden="true">·</span>
            <span>Starts a normal job in Jobs</span>
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
        <div className="automation-fields-row">
          <div>
            <label className="field-label" htmlFor="automation-run-repo">Repository</label>
            <Select id="automation-run-repo" value={repo} onChange={setRepo} disabled={pending || !repoOptions.length}>
              {repoOptions.map(item => <option key={item} value={item}>{item}</option>)}
            </Select>
          </div>
          <div>
            <label className="field-label" htmlFor="automation-run-preset">Preset</label>
            <Select id="automation-run-preset" value={preset} onChange={changePreset} disabled={pending || !presetOptions.length}>
              {presetOptions.map(item => <option key={item} value={item}>{item}</option>)}
            </Select>
          </div>
        </div>
        {declaredInputs.length > 0 && <fieldset className="preset-inputs">
          <legend className="field-label">Inputs</legend>
          <div className="preset-inputs-grid">
            {declaredInputs.map(item => <div key={item.name} className="preset-input-field">
              <label className="field-label" htmlFor={`automation-run-var-${item.name}`}>
                <code>{item.name}</code>
                {item.required ? <span className="field-required" aria-label="required">*</span> : <span className="field-optional">(optional)</span>}
              </label>
              <input
                id={`automation-run-var-${item.name}`}
                className="field-control"
                value={vars[item.name] ?? ""}
                onChange={event => setVars(current => ({ ...current, [item.name]: event.target.value }))}
                onKeyDown={submitOnShortcut}
                placeholder={item.default !== undefined ? item.default : ""}
                required={item.required}
                disabled={pending}
              />
              {item.description && <p className="field-hint">{item.description}</p>}
            </div>)}
          </div>
        </fieldset>}
        <div className="automation-fields-row">
          <div>
            <label className="field-label" htmlFor="automation-run-harness">Harness</label>
            <Select id="automation-run-harness" value={harness} onChange={changeHarness} disabled={pending}>
              <option value="">Preset default</option>
              {automationHarnesses.map(item => <option key={item} value={item}>{harnessOptionLabel(item, onlineHarnesses)}</option>)}
            </Select>
          </div>
          <div>
            <label className="field-label" htmlFor="automation-run-model">Model</label>
            <input
              id="automation-run-model"
              className="field-control"
              value={model}
              onChange={event => setModel(event.target.value)}
              placeholder="Harness default"
              disabled={pending}
            />
          </div>
        </div>
        <div>
          <div className="automation-prompt-head">
            <label className="field-label" htmlFor="automation-run-prompt">Prompt</label>
            {edited && <div className="automation-prompt-edited">
              <span className="pill pill-info">Edited for this run</span>
              <button type="button" className="link-button automation-reset" onClick={resetToSaved} disabled={pending}>
                <RotateCcw className="size-3" aria-hidden="true" />
                Reset to saved
              </button>
            </div>}
          </div>
          <textarea
            ref={textareaRef}
            id="automation-run-prompt"
            className="field-control automation-prompt-input"
            rows={8}
            value={prompt}
            onChange={event => setPrompt(event.target.value)}
            onKeyDown={submitOnShortcut}
            disabled={pending}
          />
          <p className="field-hint">
            {edited
              ? "Changes apply to this run only. The saved automation is not modified."
              : "Run with the saved configuration or override any field for this run only."}
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

export function AutomationsView({ running = new Set(), runners = [], role = "admin", onSignedOut, createOpen = false, onCloseCreate, onOpenCreate }) {
  const canRun = role !== "viewer";
  const onlineHarnesses = onlineHarnessSet(runners);
  const [automations, setAutomations] = useState([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState(null);
  const [editing, setEditing] = useState(null);
  const [confirmDelete, setConfirmDelete] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const onSignedOutRef = useRef(onSignedOut);
  onSignedOutRef.current = onSignedOut;
  const loaderRef = useRef(null);

  const refresh = useCallback(() => loaderRef.current?.refresh(), []);

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
    loaderRef.current = loader;
    const load = async () => {
      await loader.refresh();
      if (!stopped) timer = window.setTimeout(load, pollMillis);
    };
    load();
    return () => { stopped = true; loader.cancel(); window.clearTimeout(timer); };
  }, []);

  async function deleteAutomation(automation) {
    setDeleting(true);
    setError("");
    try {
      await api(automationPath(automation.name), { method: "DELETE" });
      setConfirmDelete("");
      await refresh();
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOutRef.current?.();
      else setError(failure.message);
    } finally {
      setDeleting(false);
    }
  }

  // Keep the selected automation's metadata fresh (e.g. last_ticket) without
  // overwriting the user's in-progress prompt edits inside the dialog.
  const selectedLive = selected
    ? automations.find(item => item.name === selected.name) || selected
    : null;

  return <>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading automations</p>}
    {loaded && !error && automations.length === 0 && <AutomationsEmpty canRun={canRun} onNew={onOpenCreate} />}
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
            <th scope="col" className="automation-action-col"><span className="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {automations.map(automation => <AutomationRow
            key={automation.name}
            automation={automation}
            running={running}
            now={now}
            canRun={canRun}
            confirmingDelete={confirmDelete === automation.name}
            deleting={deleting}
            onRun={item => { setConfirmDelete(""); setSelected(item); }}
            onEdit={item => { setConfirmDelete(""); setEditing(item); }}
            onAskDelete={setConfirmDelete}
            onCancelDelete={() => setConfirmDelete("")}
            onConfirmDelete={deleteAutomation}
          />)}
        </tbody>
      </table>
    </div>}
    {selectedLive && <RunAutomationDialog
      key={selectedLive.name}
      automation={selectedLive}
      running={running}
      onlineHarnesses={onlineHarnesses}
      onClose={() => setSelected(null)}
      onSignedOut={onSignedOut}
    />}
    {(createOpen || editing) && <AutomationEditorDialog
      key={editing ? `edit:${editing.name}` : "new"}
      automation={editing}
      onlineHarnesses={onlineHarnesses}
      onClose={() => { setEditing(null); onCloseCreate?.(); }}
      onSaved={refresh}
      onSignedOut={onSignedOut}
    />}
  </>;
}

export function AutomationsPage({ runners = [], role = "admin", onSignedOut }) {
  const canRun = role !== "viewer";
  const [createOpen, setCreateOpen] = useState(false);
  const progress = runningJobs(runners);
  const running = new Set(progress.keys());

  return <div className="page automations-page">
    <header className="page-head">
      <h1>Automations</h1>
      {canRun && <div className="page-actions">
        <Button type="button" onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" aria-hidden="true" />
          New automation
        </Button>
      </div>}
    </header>
    <AutomationsView
      running={running}
      runners={runners}
      role={role}
      onSignedOut={onSignedOut}
      createOpen={createOpen}
      onOpenCreate={() => setCreateOpen(true)}
      onCloseCreate={() => setCreateOpen(false)}
    />
  </div>;
}
