import React, { useEffect, useState, useId } from "react";
import Markdown from "react-markdown";
import { Check, Circle, CircleAlert, LoaderCircle } from "lucide-react";
import { reduceActivity, pollRunActivity, agentLabel, agentOutcome, agentInstanceLabels } from "./native-activity.js";

export function useRunEvents(run) {
  const [snapshot, setSnapshot] = useState({ id: run?.id, events: [], error: "", loaded: false });
  useEffect(() => {
    if (!run?.id) return;
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
          const response = await fetch(`/api/v1/runs/${encodeURIComponent(run.id)}/events?after=${cursor}`, { signal: controller.signal });
          if (!response.ok) throw new Error(`Unable to load activity (${response.status}). Retrying.`);
          const page = await response.json();
          if (stopped) return;
          setSnapshot(previous => ({ id: run.id, events: [...previous.events, ...page.events], error: "", loaded: true }));
          cursor = page.cursor;
          more = page.events.length === 128;
        }
      } catch (failure) {
        retry = true;
        if (!stopped) setSnapshot(previous => ({ ...previous, error: failure.message, loaded: true }));
      }
      if (!stopped && (retry || pollRunActivity(run))) timer = setTimeout(poll, 2000);
    }
    poll();
    return () => { stopped = true; clearTimeout(timer); controller.abort(); };
  }, [run?.id, run?.state, run?.phase, run?.completed_at]);
  const current = snapshot.id === run?.id ? snapshot : { events: [], error: "", loaded: false };
  return { ...current, ...reduceActivity(current.events, run?.state) };
}

export function RunMarkdown({ children }) {
  return <div className="run-prose"><Markdown skipHtml>{String(children || "")}</Markdown></div>;
}

const statusLabels = { running: "Running", completed: "Completed", failed: "Failed", interrupted: "Interrupted", cancelled: "Cancelled", unknown: "Status unknown" };
export function ExecutionGraph({ activities, runState, onSelect, loaded = true }) {
  const titleID = useId();
 const labels=agentInstanceLabels(activities);
  const primaryStatus = ["succeeded","awaiting_merge"].includes(runState) ? "Complete" : runState === "running" ? "Working" : runState === "failed" ? "Failed" : "Stopped";
  return <section className="execution-map" aria-labelledby={titleID}>
    <div className="run-section-heading"><h2 id={titleID}>Agents</h2><span>{activities.length} {activities.length === 1 ? "agent" : "agents"} used</span></div>
    {!activities.length ? <p className="run-empty" role="status">{!loaded ? "Loading agent activity…" : runState === "running" ? "The primary agent is working. Delegated agents will appear here." : "No subagent activity was reported for this attempt."}</p> : <>
      <div className="agent-map-primary"><strong>Primary agent</strong><span>{primaryStatus}</span></div>
      <ul className="agent-map-children" aria-label="Delegated agents">{activities.map(child => <li key={child.native_id}>
        <button type="button" className="agent-map-item" onClick={() => onSelect?.(child.native_id)} aria-label={`Read ${labels.get(child.native_id)} report`}>
          <span className="agent-map-heading"><strong>{labels.get(child.native_id)}</strong><AgentStatus child={child} /></span>
          <span className="agent-map-outcome">{agentOutcome(child)}</span>
          {!child.role && <span className="agent-role-missing">{child.name?.startsWith("/root/") ? "Agent name recorded; role unavailable" : "Role not reported"}</span>}
          <span className="agent-map-read">Read report</span>
        </button>
      </li>)}</ul>
    </>}
  </section>;
}

function AgentStatus({ child }) {
  const Icon=child.status==="completed" ? Check : child.status==="running" ? LoaderCircle : ["failed","interrupted"].includes(child.status) ? CircleAlert : Circle;
  const labels={...statusLabels,running:"Working",completed:"Complete"};
  return <span className={`agent-map-status run-status-${child.status}`}><Icon size={14} aria-hidden="true" className={child.status==="running" ? "run-spinner" : ""} />{labels[child.status] || "Status unknown"}</span>;
}

export function AgentReports({ activities, selectedID, onSelect }) {
  const [localID,setLocalID]=useState(null);
  const labels=agentInstanceLabels(activities);
 const selected=activities.find(child=>child.native_id===(selectedID || localID)) || activities[0];
  if(!selected) return <p className="run-empty">No agent reports are available for this attempt.</p>;
  return <section aria-label="Agent reports" className="agent-reports">
    <nav aria-label="Choose an agent report" className="agent-report-nav">{activities.map(child=><button type="button" key={child.native_id} aria-pressed={selected.native_id===child.native_id} onClick={()=>{setLocalID(child.native_id);onSelect?.(child.native_id);}}><span>{labels.get(child.native_id)}</span><AgentStatus child={child} /></button>)}</nav>
    <article className="agent-report-panel" aria-label={`${labels.get(selected.native_id)} report`}>
      <header className="run-section-heading"><h2>{labels.get(selected.native_id)}</h2><AgentStatus child={selected} /></header>
      <p className="run-section-description">{selected.name && !selected.name.startsWith("/root/") ? selected.name : "Full agent report"}</p>
      {!selected.role && <p className="agent-role-missing">The harness did not record this agent’s configured role.</p>}
      {selected.summary ? <RunMarkdown>{selected.summary}</RunMarkdown> : <p className="run-empty">{selected.status==="running" ? "Working. Its report will appear here." : "No report was returned."}</p>}
    </article>
  </section>;
}

export function PrimaryLog({ logs }) {
  return <section className="run-log" aria-label="Primary agent messages"><h2>Primary log</h2><p className="run-section-description">Messages from the agent coordinating this attempt.</p>{logs.length ? <ol>{logs.map((text, index) => <li key={index}><RunMarkdown>{readableMessage(text)}</RunMarkdown></li>)}</ol> : <p className="run-empty">No primary messages were reported.</p>}</section>;
}

// What a script stage printed. It is the place to look when the stage failed.
export function ScriptOutput({ scripts }) {
  return scripts.map(script => <section key={script.name} className="run-log" aria-label={`Output of the ${script.name} stage`}><h2>Output of {script.name}</h2>{script.output ? <pre className="run-script-output">{script.output}</pre> : <p className="run-empty">The script printed nothing.</p>}</section>);
}

function readableMessage(text) {
  try {
    const result = JSON.parse(text);
    if (["pr_ready", "needs_input", "no_change", "failed"].includes(result.outcome) && typeof result.summary === "string") return result.summary;
  } catch { /* Ordinary messages are already readable text. */ }
  return text;
}
