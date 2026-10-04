import React, { useEffect, useRef, useState } from "react";
import {
  Check,
  FileCode2,
  Maximize2,
  Minimize2,
  MessageSquare,
  RefreshCw,
  Square,
  X,
} from "lucide-react";
import {
  TabsRoot,
  TabsList,
  TabsTrigger,
  TabsContent,
} from "./components/ui/tabs.jsx";
import { DisclosureAction } from "./components/ui/disclosure-action.jsx";
import { Input } from "./components/ui/input.jsx";
import { Button } from "./components/ui/button.jsx";
import { displayEvents, isBusy } from "./factory-state.js";
import {
  clampPanelWidth,
  decodeGitPath,
  initialReviewTab,
  inlineMarkdown,
  markdownBlocks,
  parseDiff,
} from "./factory-review.js";
import "./factory-task-panel.css";
function Inline({ text }) {
  return inlineMarkdown(text).map((part, i) =>
    part.type === "code" ? (
      <code key={i}>{part.text}</code>
    ) : part.type === "strong" ? (
      <strong key={i}>{part.text}</strong>
    ) : part.type === "link" ? (
      <a key={i} href={part.href} target="_blank" rel="noreferrer">
        {part.text}
      </a>
    ) : (
      <React.Fragment key={i}>{part.text}</React.Fragment>
    ),
  );
}
export function ReadableMarkdown({ text }) {
  return (
    <div className="review-markdown">
      {markdownBlocks(text).map((block, i) =>
        block.type === "heading" ? (
          React.createElement(
            "h" + (block.level + 1),
            { key: i },
            <Inline text={block.text} />,
          )
        ) : block.type === "code" ? (
          <pre key={i}>
            <code>{block.text}</code>
          </pre>
        ) : block.type === "list" ? (
          React.createElement(
            block.ordered ? "ol" : "ul",
            { key: i },
            block.items.map((item, j) => (
              <li key={j}>
                <Inline text={item} />
              </li>
            )),
          )
        ) : block.type === "quote" ? (
          <blockquote key={i}>
            <Inline text={block.text} />
          </blockquote>
        ) : (
          <p key={i}>
            <Inline text={block.text} />
          </p>
        ),
      )}
    </div>
  );
}
function Changes({ detail }) {
  const [filter, setFilter] = useState("");
  const files = parseDiff(detail.diff || "");
  const represented = new Set(files.map((file) => file.path));
  for (const entry of detail.files || []) {
    const path = decodeGitPath(typeof entry === "string" ? entry : entry.path);
    if (!represented.has(path)) {
      files.push({
        path,
        added: 0,
        deleted: 0,
        lines: [
          {
            type: "meta",
            text: "No text diff is available for this entry.",
            old: null,
            new: null,
          },
        ],
      });
      represented.add(path);
    }
  }
  const visible = files.filter((file) =>
    file.path.toLowerCase().includes(filter.toLowerCase()),
  );
  return (
    <div className="review-changes">
      {detail.diff_truncated && (
        <p role="alert" className="review-notice">
          This change is too large to show in full. Review the complete diff
          before approving delivery.
        </p>
      )}
      {detail.files_truncated && (
        <p role="alert" className="review-notice">
          Only part of the file list is shown.
        </p>
      )}
      {files.length > 0 ? (
        <>
          <div className="review-file-tools">
            <label>
              <FileCode2 size={15} />
              <Input
                aria-label="Filter changed files"
                placeholder="Filter files…"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
            </label>
            <small>
              {files.length} file{files.length === 1 ? "" : "s"}
            </small>
          </div>
          {visible.map((file, i) => (
            <details key={file.path + i} className="review-file" open>
              <summary>
                <strong>{file.path}</strong>
                <span className="review-added">+{file.added}</span>
                <span className="review-deleted">−{file.deleted}</span>
                <DisclosureAction noun="diff" />
              </summary>
              <div className="review-diff-lines">
                {file.lines.map((line, j) => (
                  <div key={j} className={"review-diff-line " + line.type}>
                    <span className="review-line-number">{line.old}</span>
                    <span className="review-line-number">{line.new}</span>
                    <code>{line.text}</code>
                  </div>
                ))}
              </div>
            </details>
          ))}
          {!visible.length && (
            <p className="review-empty">No files match “{filter}”.</p>
          )}
        </>
      ) : (
        <p className="review-empty">No code changes to review yet.</p>
      )}
      {detail.files?.length > 0 && !files.length && (
        <ul>
          {detail.files.map((file) => (
            <li key={file.path || file}>{file.path || file}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
function Checks({ task, checks }) {
  const ordered = [...checks].sort(
      (a, b) => Number(a.passed) - Number(b.passed),
    ),
    failed = checks.filter((c) => !c.passed).length;
  return (
    <div className="review-checks">
      <div
        className={
          "review-check-summary " +
          (!checks.length ? "empty" : failed ? "failed" : "")
        }
      >
        <strong>
          {checks.length
            ? failed
              ? `${failed} check${failed === 1 ? "" : "s"} failed`
              : "All checks passed"
            : "No checks yet"}
        </strong>
        <small>
          {checks.length
            ? `${checks.length - failed} of ${checks.length} passed`
            : "Checks appear here after the build."}
        </small>
      </div>
      {ordered.map((check, i) => (
        <details key={check.id || check.name + i} className="review-check">
          <summary>
            <span
              className={
                check.passed ? "review-check-pass" : "review-check-fail"
              }
            >
              {check.passed ? <Check size={15} /> : <X size={15} />}
            </span>
            <strong>
              {check.name || check.title} ·{" "}
              {check.status ||
                check.result ||
                (check.passed ? "Passed" : "Failed")}
            </strong>
            {check.output && <DisclosureAction noun="log" />}
          </summary>
          {check.revision && <small>Revision {check.revision}</small>}
          <pre>{check.output || "No log was saved."}</pre>
        </details>
      ))}
      {task.review && (
        <section>
          <h3>Code review</h3>
          <ReadableMarkdown text={task.review} />
        </section>
      )}
    </div>
  );
}
export function TaskPanel({ detail, busy, close, mutate, onDiscuss }) {
  const task = detail?.task;
  const stage = task?.stage || "";
  const storageKey = task ? "machinist-task-tab:" + task.id + ":" + stage : "";
  const [tab, setTab] = useState("design"),
    [width, setWidth] = useState(() =>
      clampPanelWidth(
        localStorage.getItem("machinist-panel-width"),
        window.innerWidth,
      ),
    ),
    [expanded, setExpanded] = useState(false),
    [feedback, setFeedback] = useState(""),
    [changing, setChanging] = useState(false);
  const drag = useRef(null);
  useEffect(() => {
    if (task) {
      const saved = localStorage.getItem(storageKey);
      setTab(
        ["design", "changes", "checks"].includes(saved)
          ? saved
          : initialReviewTab(task),
      );
      setFeedback("");
      setChanging(false);
    }
  }, [task?.id, stage]);
  useEffect(() => {
    const fit = () =>
      setWidth((current) => clampPanelWidth(current, window.innerWidth));
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, []);
  function resize(next) {
    const value = clampPanelWidth(next, window.innerWidth);
    setWidth(value);
    localStorage.setItem("machinist-panel-width", String(value));
  }
  function select(next) {
    setTab(next);
    if (storageKey) localStorage.setItem(storageKey, next);
  }
  const subject =
    task?.approval_subject ||
    (stage.toLowerCase() === "design" ? "design" : "code");
  const checks = detail?.checks || task?.checks || [];
  const failed = checks.filter((check) => !check.passed).length;
  return (
    <aside
      className={
        "factory-inspector task-review-panel " + (expanded ? "expanded" : "")
      }
      style={{ "--review-width": width + "px" }}
      aria-label="Task detail"
    >
      <div
        className="review-resize"
        role="separator"
        aria-label="Resize task detail"
        aria-orientation="vertical"
        tabIndex={0}
        aria-valuemin={320}
        aria-valuemax={Math.max(320, Math.min(1000, window.innerWidth - 80))}
        aria-valuenow={width}
        onKeyDown={(e) => {
          if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) {
            e.preventDefault();
            resize(
              e.key === "Home"
                ? 320
                : e.key === "End"
                  ? 1000
                  : width + (e.key === "ArrowLeft" ? 24 : -24),
            );
          }
        }}
        onPointerDown={(e) => {
          drag.current = { x: e.clientX, width };
          e.currentTarget.setPointerCapture(e.pointerId);
        }}
        onPointerMove={(e) => {
          if (drag.current)
            resize(drag.current.width + drag.current.x - e.clientX);
        }}
        onPointerUp={(e) => {
          drag.current = null;
          e.currentTarget.releasePointerCapture(e.pointerId);
        }}
        onPointerCancel={() => {
          drag.current = null;
        }}
      />
      <header className="review-panel-header">
        <div>
          <small>
            {stage || "Task detail"}
            {task?.status && " · " + task.status.replaceAll("_", " ")}
          </small>
          <h2>{task?.title || "Loading task…"}</h2>
        </div>
        <div>
          <Button
            variant="ghost"
            size="icon"
            aria-label={expanded ? "Restore task panel" : "Expand task panel"}
            onClick={() => setExpanded(!expanded)}
          >
            {expanded ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Close task detail"
            onClick={close}
          >
            <X size={18} />
          </Button>
        </div>
      </header>
      {!task ? (
        <p role="status" className="review-empty">
          Loading task…
        </p>
      ) : (
        <>
          <TabsRoot
            value={tab}
            onValueChange={select}
            className="review-panel-content"
          >
            <TabsList className="review-tabs" aria-label="Task review">
              {[
                ["design", "Design"],
                ["changes", "Changes"],
                ["checks", "Checks"],
              ].map(([id, label]) => (
                <TabsTrigger key={id} value={id}>
                  {label}
                  {id === "checks" && failed > 0 && (
                    <span className="review-tab-count">{failed}</span>
                  )}
                </TabsTrigger>
              ))}
            </TabsList>
            <div className="review-panel-body">
              {task.error && (
                <p role="alert" className="review-notice">
                  {task.error}
                </p>
              )}
              {task.github_error && (
                <p role="alert" className="review-notice">
                  Delivery status unavailable: {task.github_error}
                </p>
              )}
              <TabsContent value={tab}>
                {tab === "design" ? (
                  <>
                    {task.brief && (
                      <details className="review-brief" open={!task.design}>
                        <summary>
                          <span>Task brief</span>
                          <DisclosureAction />
                        </summary>
                        <p>{task.brief}</p>
                      </details>
                    )}
                    {task.design ? (
                      <ReadableMarkdown
                        text={
                          typeof task.design === "string"
                            ? task.design
                            : JSON.stringify(task.design, null, 2)
                        }
                      />
                    ) : (
                      <p className="review-empty">
                        The design will appear here when planning is complete.
                      </p>
                    )}
                  </>
                ) : tab === "changes" ? (
                  <Changes detail={detail} />
                ) : (
                  <Checks task={task} checks={checks} />
                )}
              </TabsContent>
              {detail.sessions?.length > 0 && (
                <section className="review-agent-section">
                  <h3>Agent activity</h3>
                  {detail.sessions.map((session) => (
                    <details
                      key={session.id}
                      className="review-agent"
                      open={
                        !!session.permissions?.length ||
                        ["interrupted", "failed"].includes(session.status)
                      }
                    >
                      <summary>
                        <strong>
                          {session.role ||
                            (session.type === "script" ? "Check run" : "Run")}
                        </strong>
                        <small>
                          {(session.status || "Loading").replaceAll("_", " ")}
                        </small>
                        <DisclosureAction />
                      </summary>
                      {session.permissions?.map((permission) => (
                        <div key={permission.id} className="factory-permission">
                          <strong>Permission needed</strong>
                          <p>{permission.title}</p>
                          <Button
                            disabled={busy}
                            onClick={() =>
                              mutate("/permissions/" + permission.id, {
                                allow: true,
                              })
                            }
                          >
                            Allow
                          </Button>
                          <Button
                            variant="ghost"
                            disabled={busy}
                            onClick={() =>
                              mutate("/permissions/" + permission.id, {
                                allow: false,
                              })
                            }
                          >
                            Deny
                          </Button>
                        </div>
                      ))}
                      {["interrupted", "failed"].includes(session.status) && (
                        <Button
                          variant="ghost"
                          disabled={busy}
                          onClick={() => {
                            if (
                              window.confirm(
                                "Before resuming, confirm that the previous agent process has stopped on its host. Continue only after checking.",
                              )
                            )
                              mutate("/sessions/" + session.id + "/resume", {
                                confirmed_stopped: true,
                              });
                          }}
                        >
                          Resume agent
                        </Button>
                      )}
                      <pre>
                        {displayEvents(session.events || [])
                          .map((event) => event.text)
                          .join("\n\n") || "No saved output yet."}
                      </pre>
                    </details>
                  ))}
                </section>
              )}
              {task.repairs >= 3 &&
                task.status === "failed" &&
                !detail.sessions?.some(isBusy) && (
                  <section className="review-recovery">
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
              <div className="review-task-tools">
                <Button
                  variant="ghost"
                  onClick={() => onDiscuss(`About “${task.title}”: `)}
                >
                  <MessageSquare size={14} />
                  Discuss with foreman
                </Button>
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => mutate("/tasks/" + task.id + "/refresh")}
                >
                  <RefreshCw size={14} />
                  Refresh delivery status
                </Button>
                {!["done", "cancelled"].includes(task.status) && (
                  <Button
                    variant="ghost"
                    disabled={busy}
                    onClick={() => mutate("/tasks/" + task.id + "/cancel")}
                  >
                    <Square size={13} />
                    Stop task
                  </Button>
                )}
              </div>
            </div>
          </TabsRoot>
          <footer className="review-panel-footer">
            {task.pr_url && (
              <a href={task.pr_url} target="_blank" rel="noreferrer">
                Open pull request
              </a>
            )}
            {task.revision && (
              <small className="review-revision" title={task.revision}>
                Revision {task.revision.slice(0, 12)}
              </small>
            )}
            {task.status === "awaiting_approval" && (
              <div className="review-approval">
                <p>
                  {subject === "review"
                    ? "Review the diff and checks. Your approval replaces the configured agent review for this revision."
                    : subject === "code"
                      ? "Approve this revision for publication as a pull request. It stays in Review until merge is verified."
                      : "Approve this design to start the build."}
                </p>
                {changing && (
                  <label>
                    What should change?
                    <textarea
                      autoFocus
                      rows={3}
                      value={feedback}
                      onChange={(e) => setFeedback(e.target.value)}
                    />
                  </label>
                )}
                <div>
                  <Button
                    disabled={busy || changing}
                    onClick={() =>
                      mutate("/tasks/" + task.id + "/approve", {
                        version: task.version,
                        subject,
                      })
                    }
                  >
                    <Check size={15} />
                    {subject === "code"
                      ? "Approve delivery"
                      : `Approve ${subject}`}
                  </Button>
                  <Button
                    variant="outline"
                    disabled={busy || (changing && !feedback.trim())}
                    onClick={async () => {
                      if (!changing) {
                        setChanging(true);
                        return;
                      }
                      if (
                        await mutate("/tasks/" + task.id + "/changes", {
                          version: task.version,
                          message: feedback,
                        })
                      ) {
                        setFeedback("");
                        setChanging(false);
                      }
                    }}
                  >
                    Request changes
                  </Button>
                  {changing && (
                    <Button
                      variant="ghost"
                      disabled={busy}
                      onClick={() => setChanging(false)}
                    >
                      Cancel
                    </Button>
                  )}
                </div>
              </div>
            )}
          </footer>
        </>
      )}
    </aside>
  );
}
