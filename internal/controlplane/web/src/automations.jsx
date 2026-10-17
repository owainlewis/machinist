import { useEffect, useRef, useState } from "react";
import { Clock3, Play, Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { PageHeading } from "@/components/ui/page-heading";
import { State } from "./task-display.jsx";

const presets = [["", "Manual only"], ["0 * * * *", "Every hour"], ["0 9 * * *", "Daily at 09:00"], ["0 9 * * 1-5", "Weekdays at 09:00"], ["0 9 * * 1", "Mondays at 09:00"]];
const dateLabel = value => value ? new Date(value).toLocaleString(undefined, { timeZoneName: "short" }) : "";
export function automationSchedule(a) {
  if (!a.cron) return "Manual only";
  return `${presets.find(([value]) => value === a.cron)?.[1] || a.cron} · ${a.timezone}${a.paused ? " · Paused" : ""}`;
}

export function AutomationsPage({ automations = [], jobs = [], repositories = [], csrfToken, loaded, error, onRefresh }) {
  const [editor, setEditor] = useState(null);
  const [pending, setPending] = useState("");
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState(null);
  const [confirmRun, setConfirmRun] = useState("");
  const request = useRef(null);
  const opener = useRef(null);
  const wasEditing = useRef(false);
  const [conflict, setConflict] = useState(false);
  useEffect(() => { if (wasEditing.current && !editor) opener.current?.focus(); wasEditing.current = Boolean(editor); }, [editor]);
  function closeEditor() { setEditor(null); setFailure(""); setConflict(false); }
  async function mutate(path, method, body) {
    const response = await fetch(`/api/v1/automations${path}`, { method, headers: { "Content-Type": "application/json", "X-Machinist-CSRF": csrfToken }, body: JSON.stringify(body) });
    const result = await response.json().catch(() => ({}));
    if (!response.ok) { const error = new Error(result.error || `Request failed (${response.status}). Try again.`); error.status = response.status; throw error; }
    return result;
  }
  async function act(a, kind, stopped = false) {
    if (pending) return;
    setPending(a.id); setFailure(""); setNotice(null);
    try {
      if (kind === "run") {
        // Retain the key when a response is lost. A deliberate next run gets a new key.
        if (request.current?.automation !== a.id) request.current = { automation: a.id, key: crypto.randomUUID() };
        const result = await mutate(`/${a.id}/run`, "POST", { client_request_id: request.current.key, previous_process_stopped: stopped });
        request.current = null; setConfirmRun("");
        setNotice({ text: `${a.name} queued.`, id: result.id });
      } else {
        await mutate(`/${a.id}`, "PUT", { ...definition(a), paused: !a.paused });
      }
      await onRefresh();
    } catch (e) { setFailure(e.message); if (e.message.includes("previous runner processes")) setConfirmRun(a.id); if (e.status === 409) await onRefresh(); }
    finally { setPending(""); }
  }
  async function save(value, remove = false) {
    setPending("editor"); setFailure(""); setConflict(false);
    try {
      await mutate(value.id ? `/${value.id}` : "", remove ? "DELETE" : value.id ? "PUT" : "POST", remove ? { version: value.version } : definition(value));
      closeEditor(); await onRefresh();
    } catch (e) { setFailure(e.message); if (e.status === 409) { setConflict(true); await onRefresh(); } }
    finally { setPending(""); }
  }
  return <div className="runs-page automations-page">
    <PageHeading title="Automations" description="Saved agent jobs, on demand or on a schedule.">
      <Button size="sm" className="shrink-0" disabled={!loaded || !repositories.length || Boolean(pending) || Boolean(editor)} onClick={event => { opener.current = event.currentTarget; setFailure(""); setConflict(false); setEditor({ kind: "", name: "", repository: repositories[0], prompt: "", cron: "", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", paused: false }); }}><Plus className="size-3.5" />New automation</Button>
    </PageHeading>
    {((!editor && failure) || error) && <p role="alert" className="automation-error">{(!editor && failure) || error}</p>}
    {notice && <p role="status" className="automation-notice">{notice.text} <a href={`#/runs/${notice.id}`}>View run</a></p>}
    {editor && <AutomationEditor key={`${editor.id || "new"}:${editor.version || 0}`} initial={editor} repositories={repositories} pending={Boolean(pending)} error={failure} conflict={conflict} onReload={() => { const latest = automations.find(a => a.id === editor.id); if (latest) { setEditor(latest); setFailure(""); setConflict(false); } else { closeEditor(); } }} onSave={save} onClose={closeEditor} />}
    {!loaded ? <p role="status" className="py-8 text-sm text-muted-foreground">Loading automations…</p> : automations.length ? <div className="automation-list" aria-label="Saved automations">
      {automations.map(a => <AutomationRow key={a.id} automation={a} job={jobs.find(job => job.id === a.last_job)} pending={pending} editing={Boolean(editor)} confirmStopped={confirmRun === a.id} onAction={act} onEdit={event => { opener.current = event.currentTarget; setFailure(""); setConflict(false); setEditor(a); }} />)}
    </div> : !editor && <div className="automation-empty"><h2>No automations yet</h2><p>Save a prompt to review code, find bugs or handle recurring maintenance. Run it whenever you need it.</p>{!repositories.length && <a href="#/repositories">Connect a repository to get started</a>}</div>}
    <p className="automation-footnote">Jobs run from the repository root using your configured runner. Maintenance agents are read-only; the foreman applies labels and checks every merge. Schedules need Machinist running. Missed occurrences are combined into one run; unresolved jobs prevent overlapping runs. Results stay in run history, and repository changes use normal PR delivery.</p>
  </div>;
}

function AutomationRow({ automation: a, job, pending, editing, confirmStopped, onAction, onEdit }) {
  const busy = a.busy || (job && !["succeeded", "failed", "cancelled"].includes(job.state));
  const waiting = {
    queued: "Run queued.",
    running: "Run in progress.",
    awaiting_merge: "Merge the PR, then check its status.",
    awaiting_approval: "Approve the specification to continue.",
    blocked: "Resolve the blocker or cancel the task.",
  }[job?.state] || "An earlier run needs attention.";
  return <article className="automation-row" aria-label={a.name}>
    <div className="automation-info">
      <h2>{a.name}</h2>
      <p>{a.repository}{a.kind === "repository-maintenance" ? " · Repository maintenance" : ""}</p>
      <p className="automation-schedule"><Clock3 className="size-3.5 shrink-0" aria-hidden="true" />{automationSchedule(a)}</p>
      {a.next_due && <p>Next: <time dateTime={a.next_due}>{dateLabel(a.next_due)}</time></p>}
      {a.last_error && <p className="text-warning">{a.last_error}</p>}
    </div>
    <div className="automation-result">
      {a.last_job ? <>
        <div className="automation-result-heading"><a href={`#/runs/${a.last_job}`}>Last run<span className="sr-only"> {a.name}</span></a>{job && <State value={job.state} compact />}</div>
        {a.last_run && <time dateTime={a.last_run}>{dateLabel(a.last_run)}</time>}
        {busy && <span>{waiting}</span>}
      </> : <span>No runs yet</span>}
    </div>
    <div className="automation-actions">
      {(confirmStopped || a.needs_stop_confirmation) && <Button variant="outline" size="sm" disabled={Boolean(pending) || editing} onClick={() => onAction(a, "run", true)}>Confirm stopped and run</Button>}
      <Button variant="outline" size="sm" disabled={Boolean(pending) || editing || Boolean(busy)} onClick={() => onAction(a, "run")}><Play className="size-3.5" />{pending === a.id ? "Working…" : "Run now"}<span className="sr-only"> {a.name}</span></Button>
      {a.cron && <Button variant="ghost" size="sm" disabled={Boolean(pending) || editing} onClick={() => onAction(a, "pause")}>{a.paused ? "Resume" : "Pause"}<span className="sr-only"> {a.name}</span></Button>}
      <Button variant="ghost" size="sm" disabled={Boolean(pending) || editing} onClick={onEdit}>Edit<span className="sr-only"> {a.name}</span></Button>
    </div>
  </article>;
}

function definition(a) { return { kind: a.kind || "", name: a.name, repository: a.repository, prompt: a.prompt, cron: a.cron, timezone: a.timezone, paused: a.paused, version: a.version }; }
export function AutomationEditor({ initial, repositories, pending, error, conflict, onReload, onSave, onClose }) {
  const [draft, setDraft] = useState(initial);
  const [schedule, setSchedule] = useState(presets.some(([value]) => value === initial.cron) ? initial.cron : "custom");
  const [deleting, setDeleting] = useState(false);
  const errorRef = useRef(null);
  useEffect(() => { if (error) errorRef.current?.focus(); }, [error]);
  const field = (name, value) => setDraft(current => ({ ...current, [name]: value }));
  return <section className="automation-editor" aria-label={initial.id ? "Edit automation" : "New automation"}>
    <header><h2>{initial.id ? "Edit automation" : "New automation"}</h2><Button variant="ghost" size="icon" aria-label="Close editor" disabled={pending} onClick={onClose}><X className="size-4" /></Button></header>
    <form aria-busy={pending} onSubmit={e => { e.preventDefault(); onSave(draft); }}>
      {error && <div role="alert" tabIndex={-1} ref={errorRef} className="automation-error">{error}{conflict && <p>Your draft is kept. Reloading replaces it with the saved automation. <Button type="button" variant="outline" size="sm" disabled={pending} onClick={onReload}>Reload saved automation</Button></p>}</div>}
      {draft.paused && draft.cron && <p className="text-sm text-warning">Schedule paused. Save your changes, then choose Resume to enable scheduled runs. Run now remains available.</p>}
      <label>Automation type<select disabled={pending} value={draft.kind || ""} onChange={e => setDraft(current => ({ ...current, kind: e.target.value, prompt: "", name: current.name || (e.target.value === "repository-maintenance" ? "Repository maintenance" : "") }))}><option value="">Agent job</option><option value="repository-maintenance">Repository maintenance</option></select></label>
      <div className="automation-fields"><label>Name<input autoFocus required maxLength={120} readOnly={pending} value={draft.name} onChange={e => field("name", e.target.value)} placeholder="Weekly bug review" /></label><label>Repository<select required disabled={pending} value={draft.repository} onChange={e => field("repository", e.target.value)}>{repositories.map(repo => <option key={repo}>{repo}</option>)}</select></label></div>
      {draft.kind === "repository-maintenance" ? <div className="text-sm text-muted-foreground"><p>Triage open issues and PRs, add matching existing labels, and report what needs your decision. Nothing is closed automatically.</p><p className="mt-2">Auto-merge is limited to small edits to existing README and docs Markdown files from recorded Machinist runs. The current revision needs an accepted independent review, successful checks, resolved conversations, and satisfied GitHub branch protections. Everything else stays for your review.</p></div> : <label>Instructions<textarea required maxLength={32000} rows={5} readOnly={pending} value={draft.prompt} onChange={e => field("prompt", e.target.value)} placeholder="Review the code for bugs. Report findings with file references and evidence. Do not change files." /></label>}
      <div className="automation-fields"><label>Schedule<select disabled={pending} value={schedule} onChange={e => { const value = e.target.value; setSchedule(value); field("cron", value === "custom" ? "" : value); if (value) field("timezone", draft.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"); }}>{presets.map(([value,label]) => <option key={value} value={value}>{label}</option>)}<option value="custom">Custom cron</option></select></label>
        {schedule !== "" && <label>Timezone<input required disabled={pending} value={draft.timezone} onChange={e => field("timezone",e.target.value)} placeholder="Europe/London" /></label>}
      </div>
      {schedule === "custom" && <label>Cron expression<input required disabled={pending} value={draft.cron} onChange={e => field("cron", e.target.value)} placeholder="0 9 * * 1-5" /><span className="text-xs text-muted-foreground">Minute, hour, day, month, weekday. For example, 0 9 * * 1-5 runs at 09:00 on weekdays.</span></label>}
      <footer>{initial.id && <div className="automation-delete">{deleting ? <><span>Delete this automation? Existing runs stay.</span><Button type="button" variant="outline" disabled={pending} onClick={() => onSave(draft,true)}>Confirm delete</Button><Button type="button" variant="ghost" disabled={pending} onClick={() => setDeleting(false)}>Keep</Button></> : <Button type="button" variant="ghost" disabled={pending} onClick={() => setDeleting(true)}>Delete automation</Button>}</div>}<div className="automation-save"><Button type="button" variant="ghost" disabled={pending} onClick={onClose}>Cancel</Button><Button type="submit" disabled={pending}>{pending ? "Saving…" : "Save automation"}</Button></div></footer>
    </form>
  </section>;
}
