import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, CalendarClock, Eye, GitBranch, Pencil, Play, Plus, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { timeAgo } from "./format";
import type { ExecutionProfile, ManagedRepository, Pipeline, Task, Runtime, SaveTaskInput } from "./types";
import { EmptyState, ErrorState, InlineError, LoadingState, StatusBadge, ViewHeader } from "./ui";

const persistentAutoProfileID = "persistent-auto";

function profileName(profile: ExecutionProfile): string {
  return profile.id === persistentAutoProfileID ? "Automatic persistent Worker" : profile.name;
}

function profileIssue(profile: ExecutionProfile | undefined, runtime: Runtime, pipeline?: Pipeline): string {
  if (!profile) return "The selected execution profile is no longer available.";
  const compatibilityIssue = profileCompatibilityIssue(profile, runtime, pipeline);
  if (compatibilityIssue) return compatibilityIssue;
  if (profile.id === persistentAutoProfileID) return "";
  if (!profile.enabled) return "This execution profile is disabled.";
  if (!profile.healthy) return profile.health_reason || "This execution profile is unhealthy.";
  return "";
}

function profileCompatibilityIssue(profile: ExecutionProfile | undefined, runtime: Runtime, pipeline?: Pipeline): string {
  if (!profile) return "The selected execution profile is no longer available.";
  if (profile.id !== persistentAutoProfileID && profile.runtime !== runtime) return `Requires ${profile.runtime}; this Task uses ${runtime}.`;
  if ((pipeline?.stages.length ?? 1) > 1 && profile.kind !== "persistent") return "Multi-stage Pipelines require a persistent Worker.";
  return "";
}

function profileDescription(profile: ExecutionProfile | undefined): string {
  if (!profile || profile.id === persistentAutoProfileID) return "Factory selects the least-loaded eligible local or VM Worker.";
  return `${profile.runtime} · ${profile.provider} / ${profile.model}`;
}

export function TasksView({ initialID, createOpen, onRun }: { initialID?: string; createOpen?: boolean; onRun: (id: string) => void }) {
  const client = useQueryClient();
  const [editing, setEditing] = useState<Task | "new" | null>(createOpen ? "new" : null);
  const [running, setRunning] = useState<Task | null>(null);
  const [showArchived, setShowArchived] = useState(false);
  const runRequests = useRef(new Map<string, { generation: number; profileID: string; requestKey: string }>());
  const query = useQuery({ queryKey: ["tasks", showArchived], queryFn: () => api.tasks(showArchived) });
  const run = useMutation({
    mutationFn: ({ id, requestKey, profileID }: { id: string; generation: number; requestKey: string; profileID: string }) => api.runTask(id, requestKey, profileID),
    onSuccess: (detail, request) => {
      runRequests.current.delete(request.id);
      setRunning(null);
      void client.invalidateQueries({ queryKey: ["overview"] });
      void client.invalidateQueries({ queryKey: ["runs"] });
      onRun(detail.run.id);
    },
  });
  const archive = useMutation({
    mutationFn: (task: Task) => api.archiveTask(task.id, !task.archived, task.generation),
    onSuccess: () => {
      setEditing(null);
      void client.invalidateQueries({ queryKey: ["tasks"] });
      void client.invalidateQueries({ queryKey: ["overview"] });
    },
  });
  const resetArchive = archive.reset;
  const openTask = useCallback((id: string) => {
    resetArchive();
    void api.task(id).then(setEditing);
  }, [resetArchive]);
  useEffect(() => {
    if (initialID) openTask(initialID);
  }, [initialID, openTask]);
  if (query.isPending) return <LoadingState label="Loading Tasks" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  return <div className="page">
    <ViewHeader title="Tasks" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    <div className="view-toolbar task-toolbar">
      <p>A prompt you can run now or schedule across repositories.</p>
      <div className="run-toolbar-actions">
        <button className={`quiet-toggle ${showArchived ? "active" : ""}`} onClick={() => setShowArchived((value) => !value)}><Archive size={14} /> Archived</button>
        <button className="button button-primary" onClick={() => { resetArchive(); setEditing("new"); }}><Plus size={15} /> New Task</button>
      </div>
    </div>
    <InlineError error={run.error} />
    {!query.data?.length ? <EmptyState icon={<CalendarClock size={22} />} title="No Tasks yet" description="Create one prompt, choose its repositories, then run it now or on a schedule." action={<button className="button button-primary" onClick={() => setEditing("new")}><Plus size={15} /> New Task</button>} /> :
      <div className="task-list panel">
        {query.data.map((task) => <article className="task-row" key={task.id}>
          <button className="task-copy" onClick={() => openTask(task.id)}>
            <span className="task-title-line"><strong>{task.name}</strong>{task.archived && <span className="subtle-pill">Archived</span>}{task.read_only && <span className="subtle-pill">Read-only</span>}</span>
            <span>{task.prompt_preview}</span>
            <small><GitBranch size={12} /> {task.repository_count} repos · {task.pipeline_name ?? "Single agent"} · {task.runtime}</small>
          </button>
          <div className="task-schedule">
            <StatusBadge state={task.schedule.enabled ? task.schedule.health_status : "disabled"} />
            <small>{task.schedule.enabled ? `${task.schedule.cron} · ${task.schedule.timezone}` : "Manual only"}</small>
          </div>
          <div className="task-last"><span>{task.last_run_state ? <StatusBadge state={task.last_run_state} /> : "No Runs yet"}</span><small>Edited {timeAgo(task.updated_at)}</small></div>
          <div className="task-actions">
            <button className="icon-button" aria-label={`${task.read_only ? "View" : "Edit"} ${task.name}`} onClick={() => openTask(task.id)}>{task.read_only ? <Eye size={15} /> : <Pencil size={15} />}</button>
            <button className="button button-secondary" title={task.repository_count === 0 ? "Add a repository before running" : undefined} disabled={task.read_only || task.archived || task.repository_count === 0 || run.isPending} onClick={() => { run.reset(); setRunning(task); }}><Play size={14} /> Run now</button>
          </div>
        </article>)}
      </div>}
    {editing && <TaskComposer task={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void client.invalidateQueries({ queryKey: ["tasks"] }); void client.invalidateQueries({ queryKey: ["overview"] }); }} onArchive={(task) => archive.mutate(task)} archiveError={archive.error} archivePending={archive.isPending} />}
    {running && <RunTaskDialog task={running} pending={run.isPending} error={run.error} onClose={() => setRunning(null)} onRun={(profileID) => {
      const previous = runRequests.current.get(running.id);
      const requestKey = previous?.generation === running.generation && previous.profileID === profileID ? previous.requestKey : crypto.randomUUID();
      runRequests.current.set(running.id, { generation: running.generation, profileID, requestKey });
      run.mutate({ id: running.id, generation: running.generation, profileID, requestKey });
    }} />}
  </div>;
}

function RunTaskDialog({ task, pending, error, onClose, onRun }: { task: Task; pending: boolean; error: Error | null; onClose: () => void; onRun: (profileID: string) => void }) {
  const profiles = useQuery({ queryKey: ["execution-profiles"], queryFn: api.executionProfiles });
  const pipelines = useQuery({ queryKey: ["pipelines"], queryFn: api.pipelines });
  const [profileID, setProfileID] = useState(task.execution_profile_id || persistentAutoProfileID);
  const profile = profiles.data?.find((value) => value.id === profileID);
  const pipeline = pipelines.data?.find((value) => value.id === task.pipeline_id);
  const issue = profiles.isSuccess && pipelines.isSuccess ? profileIssue(profile, task.runtime, pipeline) : "";
  return <div className="modal-layer" role="presentation">
    <button className="modal-scrim" aria-label="Close Run dialog" onClick={onClose} />
    <section className="modal run-task-modal" role="dialog" aria-modal="true" aria-labelledby="run-task-title">
      <header className="modal-header"><div><h2 id="run-task-title">Run {task.name}</h2><p>Choose where this run should execute.</p></div><button className="icon-button" aria-label="Close" onClick={onClose}><X size={17} /></button></header>
      <div className="modal-body">
        <label className="field"><span>Run on</span><select aria-label="Run on" value={profileID} disabled={profiles.isPending || profiles.isError || pipelines.isPending || pipelines.isError || pending} onChange={(event) => setProfileID(event.target.value)}>
          {!profiles.data?.some((value) => value.id === profileID) && <option value={profileID}>Loading saved destination…</option>}
          {(profiles.data ?? []).map((value) => {
            const unavailable = profileIssue(value, task.runtime, pipeline);
            return <option key={value.id} value={value.id} disabled={Boolean(unavailable)}>{profileName(value)}{unavailable ? " · unavailable" : ""}</option>;
          })}
        </select><small className={issue ? "field-error" : "field-hint"}>{issue || profileDescription(profile)}</small></label>
        <p className="run-task-note">This choice applies only to this run. Scheduled runs keep the Task default.</p>
        <InlineError error={error ?? profiles.error ?? pipelines.error} />
      </div>
      <footer className="modal-footer"><button className="button button-secondary" onClick={onClose}>Cancel</button><button className="button button-primary" disabled={pending || profiles.isPending || profiles.isError || pipelines.isPending || pipelines.isError || Boolean(issue)} onClick={() => onRun(profileID)}><Play size={14} /> {pending ? "Starting…" : "Run now"}</button></footer>
    </section>
  </div>;
}

function TaskComposer({ task, onClose, onSaved, onArchive, archiveError, archivePending }: { task?: Task; onClose: () => void; onSaved: () => void; onArchive: (task: Task) => void; archiveError: Error | null; archivePending: boolean }) {
  const repositories = useQuery({ queryKey: ["repositories"], queryFn: api.repositories });
  const profiles = useQuery({ queryKey: ["execution-profiles"], queryFn: api.executionProfiles });
  const pipelines = useQuery({ queryKey: ["pipelines"], queryFn: api.pipelines });
  const readOnly = task?.read_only ?? false;
  const [name, setName] = useState(task?.name ?? "");
  const [prompt, setPrompt] = useState(task?.prompt ?? "");
  const [runtime, setRuntime] = useState<Runtime>(task?.runtime ?? "codex");
  const [profileID, setProfileID] = useState(task?.execution_profile_id ?? persistentAutoProfileID);
  const [pipelineID, setPipelineID] = useState(task?.pipeline_id ?? "00000000-0000-0000-0000-000000000001");
  const [timeout, setTimeoutValue] = useState(task?.timeout_seconds ?? 7200);
  const [concurrency, setConcurrency] = useState(task?.concurrency_limit ?? 10);
  const [selected, setSelected] = useState<string[]>(task?.repositories?.map((repository) => repository.id) ?? []);
  const [scheduled, setScheduled] = useState(task?.schedule.enabled ?? false);
  const [cron, setCron] = useState(task?.schedule.cron ?? "0 9 * * 1");
  const [timezone, setTimezone] = useState(task?.schedule.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone);
  const save = useMutation({
    mutationFn: () => {
      const input: SaveTaskInput = {
        name, prompt, runtime, timeout_seconds: timeout,
        execution_profile_id: profileID === persistentAutoProfileID ? undefined : profileID,
        pipeline_id: pipelineID,
        concurrency_limit: concurrency, repository_ids: selected,
        schedule: { enabled: scheduled, cron: scheduled ? cron : undefined, timezone: scheduled ? timezone : undefined },
        expected_generation: task?.generation,
      };
      return task ? api.updateTask(task.id, input) : api.createTask(input);
    },
    onSuccess: onSaved,
  });
  const selectedProfile = profiles.data?.find((profile) => profile.id === profileID);
  const selectedPipeline = pipelines.data?.find((pipeline) => pipeline.id === pipelineID);
  const selectedProfileIssue = profiles.isSuccess && pipelines.isSuccess ? profileIssue(selectedProfile, runtime, selectedPipeline) : "";
  const selectedProfileCompatibilityIssue = profiles.isSuccess && pipelines.isSuccess ? profileCompatibilityIssue(selectedProfile, runtime, selectedPipeline) : "";
  const dependenciesUnavailable = profiles.isPending || profiles.isError || pipelines.isPending || pipelines.isError || !selectedProfile || !selectedPipeline;
  const discard = useMutation({
    mutationFn: () => api.discardTaskOccurrence(task!.id, task!.schedule.pending_due_at!),
    onSuccess: onSaved,
  });
  const toggleRepository = (id: string) => setSelected((current) => current.includes(id) ? current.filter((value) => value !== id) : [...current, id]);
  return <div className="modal-layer" role="presentation">
    <button className="modal-scrim" aria-label="Close Task editor" onClick={onClose} />
    <section className="modal task-modal" role="dialog" aria-modal="true" aria-labelledby="task-composer-title">
      <header className="modal-header"><div><h2 id="task-composer-title">{readOnly ? "Task revision" : task ? "Edit Task" : "New Task"}</h2><p>{readOnly ? "Historical revision preserved as read-only." : "Task input processed by a reusable Pipeline."}</p></div><button className="icon-button" aria-label="Close" onClick={onClose}><X size={17} /></button></header>
      <div className="modal-body task-form">
        <label className="field"><span>Name</span><input autoFocus={!readOnly} disabled={readOnly} value={name} onChange={(event) => setName(event.target.value)} placeholder="Weekly bug scan" /></label>
        <label className="field"><span>Prompt</span><textarea className="task-prompt" disabled={readOnly} value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="Review the repository and fix..." /></label>
        <label className="field"><span>Pipeline</span><select aria-label="Pipeline" disabled={readOnly || pipelines.isPending || pipelines.isError} value={pipelineID} onChange={(event) => setPipelineID(event.target.value)}>
          {(pipelines.data ?? []).map((pipeline) => <option key={pipeline.id} value={pipeline.id}>{pipeline.name} · {pipeline.stages.length} stage{pipeline.stages.length === 1 ? "" : "s"}</option>)}
        </select><small className="field-hint">Each stage starts a fresh agent in the same worktree.</small></label>
        <div className="task-settings">
          <div className="field"><span>Runtime</span><div className="choice-control">{[["codex", "Codex"], ["claude-code", "Claude"], ["pi", "Pi"]].map(([value, label]) => <button type="button" key={value} disabled={readOnly} aria-pressed={runtime === value} onClick={() => setRuntime(value as Runtime)}>{label}</button>)}</div></div>
          <div className="field"><span>Timeout</span><div className="choice-control timeout-control">{[[1800, "30m"], [3600, "1h"], [7200, "2h"], [14400, "4h"], [28800, "8h"]].map(([value, label]) => <button type="button" key={value} disabled={readOnly} aria-pressed={timeout === value} onClick={() => setTimeoutValue(Number(value))}>{label}</button>)}</div></div>
          <label className="field"><span>Parallel sessions</span><input type="number" min={1} max={100} disabled={readOnly} value={concurrency} onChange={(event) => setConcurrency(Number(event.target.value))} /></label>
        </div>
        <label className="field"><span>Run on</span><select aria-label="Run on" disabled={readOnly || profiles.isPending || profiles.isError} value={profileID} onChange={(event) => setProfileID(event.target.value)}>
          {!profiles.data?.some((profile) => profile.id === profileID) && <option value={profileID}>Loading saved destination…</option>}
          {(profiles.data ?? []).map((profile) => {
            const issue = profileIssue(profile, runtime, selectedPipeline);
            return <option key={profile.id} value={profile.id} disabled={Boolean(issue)}>{profileName(profile)}{issue ? " · unavailable" : ""}</option>;
          })}
        </select><small className={selectedProfileIssue ? "field-error" : "field-hint"}>{selectedProfileIssue || profileDescription(selectedProfile)} This is also used by scheduled runs.</small></label>
        <div className="field"><span>Repositories</span><div className="repository-picker">{(repositories.data ?? []).map((repository: ManagedRepository) => <button type="button" key={repository.id} disabled={readOnly} className={selected.includes(repository.id) ? "selected" : ""} onClick={() => toggleRepository(repository.id)}><span className="check-mark">{selected.includes(repository.id) ? "✓" : ""}</span><GitBranch size={14} />{repository.remote_identity}</button>)}</div></div>
        <div className="schedule-card">
          <label className="switch-line"><span><strong>Schedule</strong><small>Run automatically using a five-field cron schedule.</small></span><input type="checkbox" disabled={readOnly} checked={scheduled} onChange={(event) => setScheduled(event.target.checked)} /></label>
          {scheduled && <div className="task-settings schedule-fields"><label className="field"><span>Cron</span><input className="mono" disabled={readOnly} value={cron} onChange={(event) => setCron(event.target.value)} /></label><label className="field"><span>Timezone</span><input disabled={readOnly} value={timezone} onChange={(event) => setTimezone(event.target.value)} /></label></div>}
          {task?.schedule.pending_due_at && <div className="pending-occurrence"><span><strong>{task.schedule.health_status === "disabled" ? "Occurrence paused" : "Occurrence blocked"}</strong><small>{task.schedule.health_message}</small></span>{!readOnly && (task.schedule.health_status === "blocked" || task.schedule.health_status === "disabled") && <button className="button button-danger-secondary" disabled={discard.isPending} onClick={() => discard.mutate()}>{discard.isPending ? "Discarding…" : "Discard occurrence"}</button>}</div>}
        </div>
        <InlineError error={save.error ?? archiveError ?? discard.error ?? repositories.error ?? profiles.error ?? pipelines.error} />
      </div>
      <footer className="modal-footer">{task && !readOnly && <button className="button button-danger-secondary" disabled={archivePending} onClick={() => onArchive(task)}><Archive size={14} /> {archivePending ? (task.archived ? "Restoring…" : "Archiving…") : (task.archived ? "Restore" : "Archive")}</button>}<span /><button className="button button-secondary" onClick={onClose}>{readOnly ? "Close" : "Cancel"}</button>{!readOnly && <button className="button button-primary" disabled={save.isPending || dependenciesUnavailable || !name.trim() || !prompt.trim() || (scheduled && selected.length === 0) || Boolean(selectedProfileCompatibilityIssue)} onClick={() => save.mutate()}>{save.isPending ? "Saving…" : "Save Task"}</button>}</footer>
    </section>
  </div>;
}
