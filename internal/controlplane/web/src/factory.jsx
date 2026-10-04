import React, { useEffect, useRef, useState } from "react";
import { Button } from "./components/ui/button.jsx";
import {
  createConversationLoader,
  taskDetailKey,
  mergeTaskDetail,
  displayEvents,
  factoryRequest,
  groupTasks,
  isBusy,
  stages,
} from "./factory-state.js";
import "./factory.css";
import { DisclosureAction } from "./components/ui/disclosure-action.jsx";
import { TaskPanel } from "./factory-task-panel.jsx";
import { ProjectSetup } from "./factory-setup.jsx";
import { FactoryHistory } from "./factory-history.jsx";
import { factoryView } from "./factory-state.js";
export function FactoryEntry({ LegacyApp }) {
  const [status, setStatus] = useState(null),
    [error, setError] = useState("");
  const load = () => {
    setError("");
    factoryRequest("/status")
      .then(setStatus)
      .catch((e) => setError(e.message));
  };
  useEffect(() => {
    load();
  }, []);
  if (error)
    return (
      <div className="factory-loading">
        <h1>Cannot connect to factory</h1>
        <p role="alert">{error}</p>
        <Button onClick={load}>Try again</Button>
      </div>
    );
  if (!status)
    return (
      <div className="factory-loading" role="status">
        Opening machinist…
      </div>
    );
  if (!status.enabled) return <LegacyApp />;
  return <FactoryApp status={status} onStatus={setStatus} />;
}
export function FactoryApp({ status, onStatus }) {
  const [projectID, setProjectID] = useState(
      () =>
        status.projects?.find(
          (p) => p.id === localStorage.getItem("machinist-project"),
        )?.id ||
        status.projects?.[0]?.id ||
        "",
    ),
    [project, setProject] = useState(null),
    [chat, setChat] = useState({ events: [] }),
    [view, setView] = useState(() => factoryView(window.location.hash)),
    [taskID, setTaskID] = useState(""),
    [detail, setDetail] = useState(null),
    [message, setMessage] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [connected, setConnected] = useState(true),
    [confirmedStopped, setConfirmedStopped] = useState(false),
    [dark, setDark] = useState(
      () => localStorage.getItem("machinist-theme") !== "light",
    );
  function navigate(next) {
    setView(next);
    window.location.hash = next === "history" ? "#/runs" : `#/factory/${next}`;
  }
  useEffect(() => {
    const update = () => setView(factoryView(window.location.hash));
    window.addEventListener("hashchange", update);
    return () => window.removeEventListener("hashchange", update);
  }, []);
  useEffect(() => {
    if (projectID) localStorage.setItem("machinist-project", projectID);
  }, [projectID]);
  async function projectCreated(project) {
    const nextStatus = await factoryRequest("/status");
    onStatus(nextStatus);
    setProjectID(project.id);
    navigate("chat");
  }
  const generation = useRef(0),
    requestID = useRef(null),
    refreshSequence = useRef(0),
    end = useRef(null),
    conversationLoader = useRef(null);
  if (!conversationLoader.current)
    conversationLoader.current = createConversationLoader();
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("machinist-theme", dark ? "dark" : "light");
  }, [dark]);
  function loadConversation(id) {
    return conversationLoader.current.load(id);
  }
  async function refresh(id = projectID, gen = generation.current) {
    if (!id) return;
    const sequence = ++refreshSequence.current;
    const p = await factoryRequest("/projects/" + id);
    const c = p.session?.id
      ? await loadConversation(p.session.id)
      : { events: [] };
    if (gen === generation.current && sequence === refreshSequence.current) {
      setProject(p);
      setChat(c);
    }
  }
  useEffect(() => {
    const gen = ++generation.current;
    conversationLoader.current.reset();
    setProject(null);
    setChat({ events: [] });
    setTaskID("");
    setError("");
    refresh(projectID, gen).catch((e) => {
      if (gen === generation.current) setError(e.message);
    });
  }, [projectID]);
  useEffect(() => {
    if (!chat.session?.id) return;
    const gen = generation.current;
    const source = new EventSource(
      "/api/factory/sessions/" +
        chat.session.id +
        "/events?cursor=" +
        (chat.events?.at(-1)?.id || 0),
    );
    const update = () =>
      refresh(projectID, gen).catch((e) => setError(e.message));
    let timer;
    const schedule = () => {
      if (!timer)
        timer = window.setTimeout(() => {
          timer = null;
          update();
        }, 200);
    };
    source.onmessage = schedule;
    source.addEventListener("changed", schedule);
    source.onopen = () => {
      setConnected(true);
      update();
    };
    source.onerror = () => setConnected(false);
    return () => {
      source.close();
      window.clearTimeout(timer);
    };
  }, [chat.session?.id, projectID]);
  useEffect(() => {
    setConfirmedStopped(false);
  }, [chat.session?.id, chat.session?.status]);
  useEffect(() => {
    end.current?.scrollIntoView({ block: "end" });
  }, [chat.events?.length]);
  const selectedTask = project?.tasks?.find((task) => task.id === taskID);
  const selectedTaskKey = taskDetailKey(selectedTask);
  const selectedSessionIDs = JSON.stringify(
    detail?.task?.id === taskID
      ? (detail.sessions || []).map((session) => session.id)
      : [],
  );
  useEffect(() => {
    if (!taskID) {
      setDetail(null);
      return;
    }
    let active = true;
    setDetail((current) => (current?.task?.id === taskID ? current : null));
    factoryRequest("/tasks/" + taskID)
      .then((next) => {
        if (active) setDetail((current) => mergeTaskDetail(next, current));
      })
      .catch((error) => {
        if (active) setError(error.message);
      });
    return () => {
      active = false;
    };
  }, [taskID, selectedTaskKey]);
  // Streaming refreshes only session output/permissions. Git-backed details
  // refresh when the selected task's revision or decision state changes.
  useEffect(() => {
    const sessionIDs = JSON.parse(selectedSessionIDs);
    if (!taskID || !sessionIDs.length) return;
    let active = true;
    Promise.all(
      sessionIDs.map(async (id) => {
        const conversation = await loadConversation(id);
        return {
          ...conversation.session,
          events: conversation.events,
          permissions: conversation.permissions,
        };
      }),
    )
      .then((sessions) => {
        if (active)
          setDetail((current) =>
            current?.task?.id === taskID
              ? {
                  ...current,
                  task: {
                    ...current.task,
                    activity: selectedTask?.activity ?? current.task.activity,
                    pending_permissions: selectedTask?.pending_permissions,
                  },
                  sessions,
                }
              : current,
          );
      })
      .catch((error) => {
        if (active) setError(error.message);
      });
    return () => {
      active = false;
    };
  }, [taskID, selectedSessionIDs, project]);
  async function mutate(path, body = {}) {
    setBusy(true);
    setError("");
    try {
      const result = await factoryRequest(path, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Machinist-CSRF": status.csrf_token,
        },
        body: JSON.stringify(body),
      });
      await refresh();
      return result;
    } catch (e) {
      setError(e.message);
      return null;
    } finally {
      setBusy(false);
    }
  }
  async function send(e) {
    e.preventDefault();
    if (!message.trim() || busy || isBusy(chat.session)) return;
    requestID.current ||= crypto.randomUUID();
    if (
      await mutate("/projects/" + projectID + "/messages", {
        request_id: requestID.current,
        message: message.trim(),
        ...(taskID ? { task_id: taskID } : {}),
      })
    ) {
      setMessage("");
      requestID.current = null;
      navigate("chat");
    }
  }
  const tasks = project?.tasks || [],
    groups = groupTasks(tasks),
    session = chat.session;
  return (
    <div className="factory-shell">
      <aside className="factory-sidebar">
        <a className="factory-brand" href="#/factory">
          machinist
        </a>
        <div className="factory-project-heading">
          <p className="factory-label">Projects</p>
          <button
            className="factory-add-project"
            aria-label="Add project"
            onClick={() => navigate("add")}
          >
            +
          </button>
        </div>
        <nav aria-label="Projects">
          {status.projects?.map((p) => (
            <button
              key={p.id}
              aria-current={p.id === projectID ? "page" : undefined}
              onClick={() => {
                setProjectID(p.id);
                navigate("chat");
              }}
            >
              {p.name || p.id}
            </button>
          ))}
        </nav>
        <footer>
          <button
            aria-current={
              view === "settings" || view === "history" ? "page" : undefined
            }
            onClick={() => navigate("settings")}
          >
            Settings
          </button>
          <button onClick={() => setDark(!dark)}>
            {dark ? "Dark" : "Light"} theme
          </button>
        </footer>
      </aside>
      <main className="factory-main">
        <header className="factory-header">
          <div>
            <h1>
              {view === "settings"
                ? "Settings"
                : view === "history"
                  ? "History"
                  : view === "add"
                    ? "Add a project"
                    : status.projects?.find((p) => p.id === projectID)?.name ||
                      "Factory"}
            </h1>
            <small>
              {["settings", "history", "add"].includes(view)
                ? "Factory"
                : !connected
                  ? "Reconnecting. Work continues."
                  : isBusy(session)
                    ? "Foreman is working"
                    : ["failed", "interrupted"].includes(session?.status)
                      ? "Foreman needs attention"
                      : "Foreman ready"}
            </small>
          </div>
          {["chat", "board"].includes(view) && projectID && (
            <div className="factory-tabs">
              {["chat", "board"].map((v) => (
                <button
                  key={v}
                  aria-pressed={view === v}
                  onClick={() => navigate(v)}
                >
                  {v === "chat"
                    ? "Chat"
                    : `Board${tasks.length ? " · " + tasks.length : ""}`}
                </button>
              ))}
            </div>
          )}
          {view === "history" && (
            <Button variant="ghost" onClick={() => navigate("settings")}>
              Back to settings
            </Button>
          )}
        </header>
        {error && (
          <div className="factory-error" role="alert">
            {error}
            <button aria-label="Dismiss error" onClick={() => setError("")}>
              ×
            </button>
          </div>
        )}
        {view === "add" ? (
          <ProjectSetup
            status={status}
            onCreated={projectCreated}
            onCancel={() => navigate(projectID ? "chat" : "settings")}
          />
        ) : view === "history" ? (
          <FactoryHistory />
        ) : view === "settings" ? (
          <Settings status={status} navigate={navigate} />
        ) : !projectID ? (
          <div className="factory-empty">
            <h2>Connect your first project</h2>
            <p>
              Choose an existing repository or clone from Git. Your foreman will
              manage work here.
            </p>
            <Button onClick={() => navigate("add")}>Add project</Button>
          </div>
        ) : view === "board" ? (
          <div className="factory-board">
            {stages.map((s) => (
              <section key={s}>
                <header>
                  <h2>{s}</h2>
                  <small>{groups[s].length}</small>
                </header>
                {groups[s].map((t) => (
                  <button
                    key={t.id}
                    className="factory-card"
                    onClick={() => setTaskID(t.id)}
                  >
                    <strong>{t.title}</strong>
                    <p>{t.summary || t.brief}</p>
                    <small>
                      {t.activity || t.status?.replaceAll("_", " ")}
                    </small>
                  </button>
                ))}
                {!groups[s].length && <p className="factory-muted">No tasks</p>}
              </section>
            ))}
          </div>
        ) : (
          <div className="factory-chat">
            <div className="factory-messages">
              {!chat.events?.length && (
                <div className="factory-welcome">
                  <h2>What shall we build?</h2>
                  <p>
                    Talk to your foreman. It will prepare a design, manage the
                    agents, and bring changes back for review.
                  </p>
                </div>
              )}
              {chat.permissions?.map((p) => (
                <div key={p.id} className="factory-permission">
                  <strong>Permission needed</strong>
                  <p>{p.title}</p>
                  <Button
                    disabled={busy}
                    onClick={() =>
                      mutate("/permissions/" + p.id, { allow: true })
                    }
                  >
                    Allow
                  </Button>
                  <Button
                    variant="ghost"
                    disabled={busy}
                    onClick={() =>
                      mutate("/permissions/" + p.id, { allow: false })
                    }
                  >
                    Deny
                  </Button>
                </div>
              ))}
              {displayEvents(chat.events).map((e, i) => (
                <ChatEvent
                  key={e.id || i}
                  event={e}
                  busy={busy}
                  mutate={mutate}
                />
              ))}
              <div ref={end} />
            </div>
            {tasks
              .filter(
                (t) =>
                  t.status === "awaiting_approval" ||
                  t.pending_permissions > 0 ||
                  t.activity === "Permission needed",
              )
              .map((t) => (
                <button
                  key={t.id}
                  className="factory-attention"
                  onClick={() => setTaskID(t.id)}
                >
                  {t.title}
                  <span>
                    {t.pending_permissions > 0 ||
                    t.activity === "Permission needed"
                      ? "Permission needed →"
                      : t.stage.toLowerCase() === "design"
                        ? "Review design →"
                        : "Review code →"}
                  </span>
                </button>
              ))}
            <form className="factory-composer" onSubmit={send}>
              {taskID && (
                <div className="factory-context">
                  Discussing{" "}
                  {tasks.find((t) => t.id === taskID)?.title || "task"}
                  <button
                    type="button"
                    aria-label="Clear task context"
                    onClick={() => setTaskID("")}
                  >
                    ×
                  </button>
                </div>
              )}
              <textarea
                aria-label="Message the foreman"
                value={message}
                onChange={(e) => {
                  setMessage(e.target.value);
                  requestID.current = null;
                }}
                rows={3}
                placeholder="Describe a change, ask a question, or give feedback…"
                onKeyDown={(e) => {
                  if (
                    e.key === "Enter" &&
                    !e.shiftKey &&
                    !e.nativeEvent.isComposing
                  ) {
                    e.preventDefault();
                    send(e);
                  }
                }}
              />
              <div>
                <div className="factory-composer-status">
                  <small className="factory-foreman-model">
                    {status.foreman?.name || "Foreman"} ·{" "}
                    {status.foreman?.runtime === "claude"
                      ? "Claude Code"
                      : status.foreman?.runtime || "Configured runtime"}{" "}
                    · {status.foreman?.model || "Default model"}
                  </small>
                  <small>
                    {isBusy(session)
                      ? "Foreman is working. Stop it before sending another message."
                      : "Enter to send · Shift + Enter for a new line"}
                  </small>
                </div>
                {isBusy(session) && (
                  <Button
                    type="button"
                    variant="ghost"
                    disabled={busy}
                    onClick={() =>
                      mutate("/sessions/" + session.id + "/cancel")
                    }
                  >
                    Stop
                  </Button>
                )}
                {["interrupted", "failed"].includes(session?.status) && (
                  <>
                    <label className="factory-resume-confirm">
                      <input
                        type="checkbox"
                        checked={confirmedStopped}
                        onChange={(e) => setConfirmedStopped(e.target.checked)}
                      />{" "}
                      I confirmed the previous agent process has stopped
                    </label>
                    <Button
                      type="button"
                      variant="ghost"
                      onClick={() =>
                        mutate("/sessions/" + session.id + "/resume", {
                          confirmed_stopped: confirmedStopped,
                        })
                      }
                      disabled={!confirmedStopped || busy}
                    >
                      Resume
                    </Button>
                  </>
                )}
                <Button disabled={busy || isBusy(session) || !message.trim()}>
                  Send ↑
                </Button>
              </div>
            </form>
          </div>
        )}
      </main>
      {taskID && ["chat", "board"].includes(view) && (
        <TaskPanel
          detail={detail}
          busy={busy}
          close={() => setTaskID("")}
          mutate={mutate}
          onDiscuss={(text) => {
            setMessage(text);
            navigate("chat");
          }}
        />
      )}
    </div>
  );
}
function ChatEvent({ event: e, busy, mutate }) {
  if (e.kind === "permission") return null;
  if (["activity", "context", "error", "completed", "report"].includes(e.kind))
    return (
      <details className="factory-activity" open={e.kind === "error"}>
        <summary>
          {e.title ||
            (e.kind === "context"
              ? "Task update"
              : e.text?.slice(0, 120) || e.kind)}
        </summary>
        <pre>{e.text}</pre>
      </details>
    );
  return e.text ? (
    <article className={"factory-message " + (e.kind === "user" ? "user" : "")}>
      <small>{e.kind === "user" ? "You" : "Foreman"}</small>
      <div>{e.text}</div>
    </article>
  ) : null;
}
function Settings({ status, navigate }) {
  return (
    <div className="factory-settings">
      <h2>Projects</h2>
      <p>
        Each project uses one host. Workers share its checkout and get a
        separate workspace for each task.
      </p>
      {status.projects?.map((project) => (
        <section key={project.id}>
          <strong>{project.name}</strong>
          <p>{project.path}</p>
          <small>
            {project.host === "local"
              ? "This computer"
              : status.hosts?.find((h) => h.id === project.host)?.name ||
                project.host}
          </small>
        </section>
      ))}
      <Button variant="outline" onClick={() => navigate("add")}>
        Add project
      </Button>
      <h2>Agents and pipeline</h2>
      <p>Agents and pipelines are configuration. Changes apply to new tasks.</p>
      <h3>Agents</h3>
      {status.agents?.map((a) => (
        <section key={a.id || a.name}>
          <strong>{a.name || a.id}</strong>
          <p>{a.description}</p>
          <small>
            {a.runtime} · {a.model || "Default model"}
          </small>
          {a.prompt && (
            <details>
              <summary>
                <span>Instructions</span>
                <DisclosureAction />
              </summary>
              <pre>{a.prompt}</pre>
            </details>
          )}
        </section>
      ))}
      <h3>Pipeline</h3>
      {status.pipelines?.map((p) => (
        <section key={p.id || p.name}>
          <strong>{p.name || p.id}</strong>
          <ol>
            {p.steps?.map((s) => (
              <li key={s.id || s.name}>
                {s.name || s.id}{" "}
                <small>
                  {s.kind || s.type}
                  {s.agent ? " · " + s.agent : ""}
                </small>
              </li>
            ))}
          </ol>
        </section>
      ))}
      <h3>Runtime</h3>
      <p>
        {status.runtime_error ||
          status.runtime?.status ||
          "Configured local runtime"}
      </p>
      <p>
        Host setup is operator managed. Submit and review work in the browser.
      </p>
      <h3>Execution hosts</h3>
      {(status.hosts || [{ id: "local", name: "This computer" }]).map(
        (host) => (
          <section key={host.id}>
            <strong>
              {host.id === "local" ? "This computer" : host.name || host.id}
            </strong>
            <p>
              {host.id === "local"
                ? "Workers run locally."
                : "Workers connect through SSH. Credentials and build tools belong to this host."}
            </p>
          </section>
        ),
      )}
      <h3>History</h3>
      <p>Inspect previous batch runs without leaving the factory.</p>
      <Button variant="outline" onClick={() => navigate("history")}>
        View history
      </Button>
    </div>
  );
}
