import React, { useEffect, useRef, useState } from "react";
import { Button } from "./components/ui/button.jsx";
import {
  displayEvents,
  factoryRequest,
  groupTasks,
  isBusy,
  stages,
} from "./factory-state.js";
import "./factory.css";
export function FactoryEntry({ LegacyApp }) {
  const [status, setStatus] = useState(null),
    [error, setError] = useState(""),
    [hash, setHash] = useState(window.location.hash);
  const load = () => {
    setError("");
    factoryRequest("/status")
      .then(setStatus)
      .catch((e) => setError(e.message));
  };
  useEffect(() => {
    load();
    const fn = () => setHash(window.location.hash);
    window.addEventListener("hashchange", fn);
    return () => window.removeEventListener("hashchange", fn);
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
  if (
    !status.enabled ||
    (hash.startsWith("#/") && !hash.startsWith("#/factory"))
  )
    return (
      <>
        {status.enabled ? (
          <a className="factory-return" href="#/factory">
            ← Back to factory
          </a>
        ) : (
          <div className="factory-return">
            Enable [factory] in configuration to use foreman chat.
          </div>
        )}
        <LegacyApp />
      </>
    );
  return <FactoryApp status={status} />;
}
export function FactoryApp({ status }) {
  const [projectID, setProjectID] = useState(status.projects?.[0]?.id || ""),
    [project, setProject] = useState(null),
    [chat, setChat] = useState({ events: [] }),
    [view, setView] = useState("chat"),
    [taskID, setTaskID] = useState(""),
    [detail, setDetail] = useState(null),
    [message, setMessage] = useState(""),
    [feedback, setFeedback] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [connected, setConnected] = useState(true),
    [confirmedStopped, setConfirmedStopped] = useState(false),
    [dark, setDark] = useState(
      () => localStorage.getItem("machinist-theme") !== "light",
    );
  const generation = useRef(0),
    requestID = useRef(null),
    refreshSequence = useRef(0),
    end = useRef(null);
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("machinist-theme", dark ? "dark" : "light");
  }, [dark]);
  async function loadConversation(id) {
    let cursor = 0,
      result = { events: [] };
    while (true) {
      const page = await factoryRequest(
        "/sessions/" + id + "?cursor=" + cursor,
      );
      result = { ...page, events: [...result.events, ...(page.events || [])] };
      if (!page.events?.length || page.events.length < 100) break;
      const next = page.events.at(-1).id;
      if (next <= cursor) break;
      cursor = next;
    }
    return result;
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
      "/api/factory/sessions/" + chat.session.id + "/events",
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
  useEffect(() => {
    if (!taskID) {
      setDetail(null);
      return;
    }
    let active = true;
    setDetail((current) => (current?.task?.id === taskID ? current : null));
    factoryRequest("/tasks/" + taskID)
      .then(async (d) => {
        const sessions = await Promise.all(
          (d.sessions || []).map(async (session) => ({
            ...session,
            ...(await loadConversation(session.id)),
          })),
        );
        if (active)
          setDetail({
            ...d,
            sessions: sessions.map((v) => ({
              ...v.session,
              events: v.events,
              permissions: v.permissions,
            })),
          });
      })
      .catch((e) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [taskID, project?.tasks]);
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
      setView("chat");
    }
  }
  const tasks = project?.tasks || [],
    groups = groupTasks(tasks),
    task = detail?.task,
    session = chat.session,
    subject =
      task?.approval_subject || (task?.stage === "Design" ? "design" : "code");
  return (
    <div className="factory-shell">
      <aside className="factory-sidebar">
        <a className="factory-brand" href="#/factory">
          machinist<small>factory</small>
        </a>
        <p className="factory-label">Projects</p>
        <nav aria-label="Projects">
          {status.projects?.map((p) => (
            <button
              key={p.id}
              aria-current={p.id === projectID ? "page" : undefined}
              onClick={() => {
                setProjectID(p.id);
                setView("chat");
              }}
            >
              {p.name || p.id}
            </button>
          ))}
        </nav>
        <footer>
          <button onClick={() => setView("settings")}>Settings</button>
          <a href="#/runs">Execution history</a>
          <button onClick={() => setDark(!dark)}>
            {dark ? "Dark" : "Light"} theme
          </button>
        </footer>
      </aside>
      <main className="factory-main">
        <header className="factory-header">
          <div>
            <h1>
              {status.projects?.find((p) => p.id === projectID)?.name ||
                "Factory"}
            </h1>
            <small>
              {!connected
                ? "Reconnecting. Work continues."
                : session?.status?.replaceAll("_", " ") || "Local foreman"}
            </small>
          </div>
          <div className="factory-tabs">
            {["chat", "board"].map((v) => (
              <button
                key={v}
                aria-pressed={view === v}
                onClick={() => setView(v)}
              >
                {v === "chat"
                  ? "Chat"
                  : `Board${tasks.length ? " · " + tasks.length : ""}`}
              </button>
            ))}
          </div>
        </header>
        {error && (
          <div className="factory-error" role="alert">
            {error}
            <button aria-label="Dismiss error" onClick={() => setError("")}>
              ×
            </button>
          </div>
        )}
        {view === "settings" ? (
          <Settings status={status} />
        ) : !projectID ? (
          <div className="factory-empty">
            <h2>Connect your first project</h2>
            <p>
              Add a project and repository to factory configuration. Your
              foreman will manage work here.
            </p>
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
                <small>
                  {isBusy(session)
                    ? "Foreman is working. Stop it before sending another message."
                    : "Enter to send · Shift + Enter for a new line"}
                </small>
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
      {taskID && (
        <aside className="factory-inspector" aria-label="Task detail">
          <header>
            <button
              aria-label="Close task detail"
              onClick={() => setTaskID("")}
            >
              ←
            </button>
            <strong>Task detail</strong>
          </header>
          {!task ? (
            <p role="status">Loading task…</p>
          ) : (
            <div>
              <h2>{task.title}</h2>
              <small>
                {task.stage} · {task.status?.replaceAll("_", " ")}
              </small>
              <p>{task.brief}</p>
              {task.error && (
                <p role="alert" className="factory-error">
                  {task.error}
                </p>
              )}
              {task.github_error && (
                <p role="alert">
                  Delivery status unavailable: {task.github_error}
                </p>
              )}
              {task.review && (
                <section>
                  <h3>Review</h3>
                  <pre>{task.review}</pre>
                </section>
              )}
              {task.design && (
                <section>
                  <h3>Design</h3>
                  <pre>
                    {typeof task.design === "string"
                      ? task.design
                      : JSON.stringify(task.design, null, 2)}
                  </pre>
                </section>
              )}
              {detail.diff && (
                <section>
                  <h3>Changes</h3>
                  <pre className="factory-diff">{detail.diff}</pre>
                </section>
              )}
              {detail.files?.length > 0 && (
                <section>
                  <h3>Files</h3>
                  {detail.files.map((f) => (
                    <p key={f.path || f}>{f.path || f}</p>
                  ))}
                </section>
              )}
              {(detail.checks || task.checks)?.length > 0 && (
                <section>
                  <h3>Checks</h3>
                  {(detail.checks || task.checks).map((c, i) => (
                    <div key={c.id || i}>
                      {c.name || c.title} ·{" "}
                      {c.status || c.result || (c.passed ? "Passed" : "Failed")}
                      {c.output && (
                        <details>
                          <summary>Output</summary>
                          <pre>{c.output}</pre>
                        </details>
                      )}
                    </div>
                  ))}
                </section>
              )}
              {task.pr_url && (
                <a href={task.pr_url} target="_blank" rel="noreferrer">
                  Open pull request →
                </a>
              )}
              {task.revision && (
                <p>
                  <small>Revision {task.revision}</small>
                </p>
              )}
              {task.status === "awaiting_approval" && (
                <section className="factory-decision">
                  <h3>
                    {subject === "review"
                      ? "Human code review"
                      : `Review ${subject}`}
                  </h3>
                  {subject === "review" && (
                    <p>
                      Review the diff and checks. Your approval replaces the
                      configured agent review for this revision.
                    </p>
                  )}
                  {subject === "code" && (
                    <p>
                      Approve this revision for publication as a pull request.
                      It stays in Review until merge is verified.
                    </p>
                  )}
                  <Button
                    disabled={busy}
                    onClick={() =>
                      mutate("/tasks/" + task.id + "/approve", {
                        version: task.version,
                        subject,
                      })
                    }
                  >
                    {subject === "code"
                      ? "Approve delivery"
                      : `Approve ${subject}`}
                  </Button>
                  <label>
                    What should change?
                    <textarea
                      rows={3}
                      value={feedback}
                      onChange={(e) => setFeedback(e.target.value)}
                    />
                  </label>
                  <Button
                    disabled={busy || !feedback.trim()}
                    variant="ghost"
                    onClick={async () => {
                      if (
                        await mutate("/tasks/" + task.id + "/changes", {
                          version: task.version,
                          message: feedback,
                        })
                      )
                        setFeedback("");
                    }}
                  >
                    Request changes
                  </Button>
                </section>
              )}
              {task.repairs >= 3 &&
                task.status === "failed" &&
                !detail.sessions?.some(isBusy) && (
                  <section className="factory-decision">
                    <h3>Repair limit reached</h3>
                    <p>
                      Three repairs failed. Review the evidence before allowing
                      another attempt.
                    </p>
                    <Button
                      disabled={busy}
                      onClick={() =>
                        mutate("/tasks/" + task.id + "/continue", {
                          version: task.version,
                        })
                      }
                    >
                      Continue after review
                    </Button>
                  </section>
                )}
              <section>
                <Button
                  variant="ghost"
                  onClick={() => {
                    setMessage(`About “${task.title}”: `);
                    setView("chat");
                  }}
                >
                  Discuss with foreman
                </Button>
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => mutate("/tasks/" + task.id + "/refresh")}
                >
                  Refresh delivery status
                </Button>
                {!["done", "cancelled"].includes(task.status) && (
                  <Button
                    variant="ghost"
                    disabled={busy}
                    onClick={() => mutate("/tasks/" + task.id + "/cancel")}
                  >
                    Stop task
                  </Button>
                )}
              </section>
              {detail.sessions?.length > 0 && (
                <section>
                  <h3>Agent activity</h3>
                  {detail.sessions.map((s) => (
                    <details
                      key={s.id}
                      open={
                        !!s.permissions?.length ||
                        ["interrupted", "failed"].includes(s.status)
                      }
                    >
                      <summary>
                        {s.role} · {s.status}
                      </summary>
                      {s.permissions?.map((p) => (
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
                      {["interrupted", "failed"].includes(s.status) && (
                        <Button
                          variant="ghost"
                          disabled={busy}
                          onClick={() => {
                            if (
                              window.confirm(
                                "Before resuming, confirm that the previous agent process has stopped on its host. Continue only after checking.",
                              )
                            )
                              mutate("/sessions/" + s.id + "/resume", {
                                confirmed_stopped: true,
                              });
                          }}
                        >
                          Resume agent
                        </Button>
                      )}
                      <pre>
                        {displayEvents(s.events || [])
                          .map((e) => e.text)
                          .join("\n\n") || "No saved output yet."}
                      </pre>
                    </details>
                  ))}
                </section>
              )}
            </div>
          )}
        </aside>
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
function Settings({ status }) {
  return (
    <div className="factory-settings">
      <h2>Factory configuration</h2>
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
              <summary>Instructions</summary>
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
      <a href="#/workers">View workers →</a>
    </div>
  );
}
