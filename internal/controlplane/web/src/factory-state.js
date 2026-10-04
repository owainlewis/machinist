export const stages = ["Design", "Build", "Review", "Done"];
export function groupTasks(tasks = []) {
  const g = Object.fromEntries(stages.map((s) => [s, []]));
  for (const t of tasks)
    g[
      stages.find((s) => s.toLowerCase() === String(t.stage).toLowerCase()) ||
        "Design"
    ].push(t);
  return g;
}
export function displayEvents(events = []) {
  const out = [];
  for (const e of events) {
    if (e.kind === "activity" && !e.title?.trim() && !e.text?.trim()) continue;
    const last = out.at(-1);
    if (
      e.kind === "activity" &&
      last?.kind === "activity" &&
      e.title === last.title &&
      e.text === last.text &&
      e.status === last.status
    )
      continue;
    if (
      e.kind === "message_delta" &&
      last?.kind === e.kind &&
      last.turn_id === e.turn_id
    )
      last.text += e.text || "";
    else out.push({ ...e });
  }
  return out;
}
export function isBusy(s) {
  return [
    "queued",
    "running",
    "awaiting_permission",
    "stop_requested",
  ].includes(s?.status);
}
export async function factoryRequest(path, options = {}) {
  const r = await fetch("/api/factory" + path, options);
  const b = await r.json().catch(() => ({}));
  if (!r.ok) throw Error(b.error || `Request failed (${r.status})`);
  return b;
}

export function factoryView(hash = "") {
  if (hash.startsWith("#/runs")) return "history";
  if (
    [
      "#/workers",
      "#/analytics",
      "#/triggers",
      "#/workflows",
      "#/commands",
    ].some((route) => hash.startsWith(route))
  )
    return "settings";
  if (hash === "#/factory/settings") return "settings";
  if (hash === "#/factory/add") return "add";
  if (hash === "#/factory/board") return "board";
  return "chat";
}

// Keep one cursor per session. Serialize reads so overlapping stream updates
// cannot append the same events twice or overwrite newer session metadata.
export function createConversationLoader(request = factoryRequest) {
  let sessions = new Map();
  return {
    reset() {
      sessions = new Map();
    },
    load(id) {
      const cache = sessions;
      let entry = cache.get(id);
      if (!entry) {
        entry = {
          cursor: 0,
          conversation: { events: [] },
          pending: Promise.resolve(),
        };
        cache.set(id, entry);
      }
      const next = entry.pending
        .catch(() => {})
        .then(async () => {
          let cursor = entry.cursor;
          let conversation = entry.conversation;
          while (true) {
            const page = await request(
              `/sessions/${encodeURIComponent(id)}?cursor=${cursor}`,
            );
            const events = (page.events || []).filter(
              (event) => event.id > cursor,
            );
            conversation = {
              ...page,
              events: [...conversation.events, ...events],
            };
            const nextCursor = events.at(-1)?.id || cursor;
            if (nextCursor === cursor || (page.events || []).length < 100) {
              cursor = nextCursor;
              break;
            }
            cursor = nextCursor;
          }
          entry.cursor = cursor;
          entry.conversation = conversation;
          return conversation;
        });
      entry.pending = next;
      return next;
    },
  };
}

// Activity text changes with streamed tool output. It does not invalidate a
// Git diff; revision, checks, and human decision state do.
export function taskDetailKey(task) {
  if (!task) return "";
  return JSON.stringify([
    task.id,
    task.version,
    task.stage,
    task.status,
    task.step,
    task.revision,
    task.approval_subject,
    task.design,
    task.review,
    task.pr_url,
    task.github_error,
    task.check_state,
  ]);
}

// A slower task/diff read must not discard newer streamed session output.
export function mergeTaskDetail(next, current) {
  if (current?.task?.id !== next.task?.id) return next;
  const loaded = new Map(
    (current.sessions || []).map((session) => [session.id, session]),
  );
  return {
    ...next,
    sessions: (next.sessions || []).map((session) => {
      const saved = loaded.get(session.id);
      return saved?.events ? { ...session, ...saved } : session;
    }),
  };
}
