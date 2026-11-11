import React, { useCallback, useEffect, useMemo, useState } from "react";
import Markdown from "react-markdown";
import { AssistantRuntimeProvider, ComposerPrimitive, MessagePrimitive, ThreadPrimitive, useExternalStoreRuntime } from "@assistant-ui/react";
import { ArrowDown, ArrowLeft, ArrowUp, Check, ChevronRight, CircleAlert, LoaderCircle, Minus, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import { useRunEvents, ExecutionGraph, AgentReports, PrimaryLog, ScriptOutput } from "./native-activity.jsx";
import { formatCost, formatDurationMillis, formatTokens, runDuration, stageStatus, ticketFigures } from "./run-metrics.js";
import { State } from "./task-display.jsx";
import { isActive, issueLink, prLink, ticketSource } from "./task-presentation.js";
import { ticketThread } from "./ticket-thread.js";

const pollMillis = 2000;
const finishedHint = "This ticket is finished";
const composerHints = { working: "The agent is working", needs_you: "Reply to continue this ticket", pr_ready: "Comment on the pull request to ask for changes", done: finishedHint, failed: finishedHint, cancelled: finishedHint };

export function RunActivity({ run }) {
  const data = useRunEvents({ id: run.id, state: run.status });
  const [selected, setSelected] = useState(null);
  return <div className="run-activity">
    {data.error && <p className="form-error" role="alert">{data.error}</p>}
    <ExecutionGraph activities={data.children} runState={run.status} loaded={data.loaded} onSelect={setSelected} />
    {data.children.length > 0 && <AgentReports activities={data.children} selectedID={selected} onSelect={setSelected} />}
    <ScriptOutput scripts={data.scripts} />
    <PrimaryLog logs={data.logs} />
  </div>;
}

function runIcon(status) {
  if (status === "running" || status === "queued") return [LoaderCircle, "text-primary state-working"];
  return status === "succeeded" ? [Check, "text-success"] : [CircleAlert, "text-warning"];
}

const stageIcons = { good: [Check, "text-success"], bad: [CircleAlert, "text-warning"], muted: [Minus, ""] };

// The stages of a job, in the order they ran. They arrive when the run ends.
function RunStages({ stages }) {
  return <ol className="run-stages" aria-label="Stages">{stages.map(stage => {
    const { label, tone } = stageStatus(stage);
    const [Icon, color] = stageIcons[tone];
    return <li key={stage.name} className="run-stage">
      <Icon className={`size-3.5 shrink-0 ${color}`} aria-hidden="true" />
      <strong>{stage.name}</strong>
      <span>{stage.kind}</span>
      <span>{label}</span>
      {stage.state !== "skipped" && <span>{formatDurationMillis(stage.duration_millis ?? 0)}</span>}
      {stage.tokens !== undefined && <span>{formatTokens(stage.tokens)} tokens</span>}
      {stage.cost !== undefined && <span>{formatCost(stage.cost)}</span>}
      {stage.error && <p className="run-stage-error">{stage.error}</p>}
    </li>;
  })}</ol>;
}

// A run sits in the conversation the way a tool call does: one line that opens.
function RunStep({ data: run }) {
  const [open, setOpen] = useState(false);
  const name = run.kind === "reviewer" ? `Reviewer: ${run.reviewer}` : "Agent run";
  const [Icon, tone] = runIcon(run.status);
  return <div className="chat-step">
    <button type="button" className="chat-step-row" aria-expanded={open} disabled={run.status === "queued"} onClick={() => setOpen(value => !value)}>
      <Icon className={`size-3.5 shrink-0 ${tone}`} aria-hidden="true" />
      <strong>{name}</strong>
      <span>{run.status === "queued" ? "Waiting for a runner" : `${run.status.replaceAll("_", " ")} on ${run.runner || "a runner"}`}</span>
      <span>{runDuration(run)}</span>
      {run.tokens !== undefined && <span>{formatTokens(run.tokens)} tokens</span>}
      {run.cost !== undefined && <span>{formatCost(run.cost)}</span>}
      <ChevronRight className="chat-step-chevron size-3.5" aria-hidden="true" />
    </button>
    {run.stages?.length > 1 && <RunStages stages={run.stages} />}
    {open && <RunActivity run={run} />}
  </div>;
}

const ChatText = ({ text }) => <div className="run-prose"><Markdown skipHtml>{text}</Markdown></div>;
const partComponents = { Text: ChatText, data: { by_name: { run: RunStep } } };

function UserMessage({ author }) {
  return <MessagePrimitive.Root className="chat-user">
    {author && <div className="chat-author">{author}</div>}
    <div className="chat-bubble"><MessagePrimitive.Parts components={partComponents} /></div>
  </MessagePrimitive.Root>;
}

function ChatMessage({ message }) {
  if (message.role === "user") return <UserMessage author={message.metadata?.custom?.author} />;
  return <MessagePrimitive.Root className={message.role === "system" ? "chat-system" : "chat-assistant"}><MessagePrimitive.Parts components={partComponents} /></MessagePrimitive.Root>;
}

function TicketChat({ ticket, entries, runs, onChanged, onCancel, onError }) {
  const messages = useMemo(() => ticketThread(entries, runs), [entries, runs]);
  const working = ticket.state === "working";
  const runtime = useExternalStoreRuntime({
    messages,
    convertMessage: message => message,
    isRunning: working,
    isDisabled: ticket.state !== "needs_you",
    onNew: async message => {
      const text = message.content.filter(part => part.type === "text").map(part => part.text).join("\n");
      onError?.("");
      try {
        await api(`/api/v1/tickets/${encodeURIComponent(ticket.id)}/answer`, { method: "POST", body: { text } });
        await onChanged?.();
      } catch (failure) {
        onError?.(failure.message);
        runtime.thread.composer.setText(text);
      }
    },
  });
  return <AssistantRuntimeProvider runtime={runtime}>
    <ThreadPrimitive.Root className="chat-thread">
      <ThreadPrimitive.Viewport className="chat-viewport">
        <div className="chat-messages">
          <ThreadPrimitive.Messages>{({ message }) => <ChatMessage message={message} />}</ThreadPrimitive.Messages>
          {working && <p className="chat-working" role="status">Working</p>}
        </div>
        <ThreadPrimitive.ViewportFooter className="chat-footer">
          <ThreadPrimitive.ScrollToBottom className="chat-scroll" aria-label="Scroll to the latest message"><ArrowDown className="size-4" aria-hidden="true" /></ThreadPrimitive.ScrollToBottom>
          <ComposerPrimitive.Root className="chat-composer">
            <ComposerPrimitive.Input className="chat-input" rows={1} placeholder={composerHints[ticket.state]} aria-label="Your answer" />
            <div className="chat-composer-row">
              {working
                ? <button type="button" className="chat-send" aria-label="Stop and cancel ticket" onClick={onCancel}><Square className="size-3.5" fill="currentColor" aria-hidden="true" /></button>
                : <ComposerPrimitive.Send className="chat-send" aria-label="Send reply"><ArrowUp className="size-4" aria-hidden="true" /></ComposerPrimitive.Send>}
            </div>
          </ComposerPrimitive.Root>
        </ThreadPrimitive.ViewportFooter>
      </ThreadPrimitive.Viewport>
    </ThreadPrimitive.Root>
  </AssistantRuntimeProvider>;
}

export function TicketView({ detail, onChanged, onCancel, onError, actionError }) {
  const { ticket, entries, runs, figures } = detail;
  const issue = issueLink(ticket);
  const pr = prLink(ticket);
  return <div className="chat-page">
    <header className="chat-header">
      <a className="icon-button shrink-0" href="#/work" aria-label="Back to Work"><ArrowLeft className="size-4" aria-hidden="true" /></a>
      <div className="chat-heading">
        <h1>{ticket.title || ticket.id}</h1>
        <div className="ticket-facts">
          <State value={ticket.state} />
          <span>{ticket.repo}</span>
          {issue ? <a href={issue} target="_blank" rel="noreferrer">{ticketSource(ticket)}</a> : <span>{ticketSource(ticket)}</span>}
          {pr && <a href={pr} target="_blank" rel="noreferrer">PR #{ticket.pr_number}</a>}
          <span>From {ticket.created_by}</span>
        </div>
      </div>
      <details className="chat-details">
        <summary>Details</summary>
        <dl>{ticketFigures(figures).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      </details>
      {isActive(ticket) && ticket.state !== "working" && <Button variant="outline" size="sm" onClick={onCancel}>Cancel ticket</Button>}
    </header>
    {actionError && <p className="form-error chat-error" role="alert">{actionError}</p>}
    <TicketChat ticket={ticket} entries={entries} runs={runs} onChanged={onChanged} onCancel={onCancel} onError={onError} />
  </div>;
}

export function TaskDetail({ ticketID, onSignedOut, onChanged }) {
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState("");
  const refresh = useCallback(async () => {
    try {
      const next = await api(`/api/v1/tickets/${encodeURIComponent(ticketID)}`);
      setDetail(next);
      setError("");
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    }
  }, [ticketID, onSignedOut]);
  useEffect(() => {
    setDetail(null);
    setError("");
    setActionError("");
    let stopped = false;
    let timer;
    const load = async () => {
      await refresh();
      if (!stopped) timer = window.setTimeout(load, pollMillis);
    };
    load();
    return () => { stopped = true; window.clearTimeout(timer); };
  }, [refresh]);
  async function changed() {
    await refresh();
    await onChanged?.();
  }
  async function cancel() {
    if (!window.confirm("Cancel this ticket and stop any run in progress? Its pull request stays open.")) return;
    setActionError("");
    try {
      await api(`/api/v1/tickets/${encodeURIComponent(ticketID)}/cancel`, { method: "POST" });
      await changed();
    } catch (failure) {
      setActionError(failure.message);
    }
  }
  if (!detail) return <div className="page">
    <a className="back-link" href="#/work"><ArrowLeft className="size-3.5" aria-hidden="true" />Work</a>
    {error ? <p className="form-error" role="alert">{error}</p> : <p className="page-note" role="status">Loading ticket</p>}
  </div>;
  return <TicketView detail={detail} onChanged={changed} onCancel={cancel} onError={setActionError} actionError={actionError} />;
}
