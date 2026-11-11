export function reduceActivity(events, runState) {
  // Raw history is retained. Reconstruct identity classification and original
  // names so pre-fix runs get the same graph without rewriting their history.
  const native = nativeIdentities(events);
  const children = new Map();
  const logs = [];
 const reports=new Map();
  for (const event of events) {
    if (event.type === "agent.activity") {
      try {
        const activity = JSON.parse(event.message);
        if (!activity.native_id || native.roots.has(activity.native_id)) continue;
        if (native.classified && !native.agents.has(activity.native_id)) continue;
        const definition = native.agents.get(activity.native_id);
        if (definition?.name) activity.name = definition.name;
 if (definition?.role) activity.role=definition.role;
        const prior = children.get(activity.native_id);
        if (activity.status === "unknown" && prior) activity.status = prior.status;
        if (prior?.name) activity.name = prior.name;
 if (!activity.role && prior?.role) activity.role=prior.role;
        children.set(activity.native_id, { ...prior, ...activity, started_at: prior?.started_at || event.time, updated_at: event.time });
      } catch { /* Raw event remains available in diagnostics. */ }
    }
    if (event.type === "agent.message") {
      try { const message = JSON.parse(event.message); if (message.primary) logs.push(message.text); else if(message.native_id && message.text) reports.set(message.native_id,message.text); } catch { /* Ignore malformed display data. */ }
    }
  }
  const stopped = ["interrupted", "cancelled", "timed_out"].includes(runState);
  return { scripts: scriptOutputs(events), children: [...children.values()].map(child => { const report=native.messages.get(child.native_id) || native.messages.get(native.agents.get(child.native_id)?.tool) || reports.get(child.native_id) || reports.get(native.agents.get(child.native_id)?.tool) || native.reports.get(child.native_id) || child.summary || ""; return {...child,summary:report,status: stopped && child.status === "running" ? "interrupted" : child.status}; }), logs };
}

const scriptOutputLimit = 16000;

// What each script stage printed, stdout and stderr in the order they arrived.
// A stage.started event marks where the output of a stage begins. Long output
// keeps its end, which is where a failure is reported.
function scriptOutputs(events) {
  const scripts = [];
  let current = null;
  for (const event of events) {
    if (event.type === "stage.started") {
      const [, name, kind] = /^name=(.*) kind=(\w+)$/.exec(event.message || "") || [];
      current = kind === "script" ? { name, output: "", decoders: new Map() } : null;
      if (current) scripts.push(current);
    }
    if (!current || event.type !== "process.output" || event.encoding !== "base64") continue;
    try {
      const key = event.stream || "stdout";
      if (!current.decoders.has(key)) current.decoders.set(key, new TextDecoder());
      const bytes = Uint8Array.from(atob(event.data), char => char.charCodeAt(0));
      current.output = (current.output + current.decoders.get(key).decode(bytes, {stream: true})).slice(-scriptOutputLimit);
    } catch { /* Malformed raw output does not hide the rest. */ }
  }
  return scripts.map(({ name, output }) => ({ name, output }));
}

function nativeIdentities(events) {
  const agents = new Map(), roots = new Set(), streams = new Map(), toolRoles=new Map(), reports=new Map(), roles=new Map(), messages=new Map();
  let classified = false;
  const observe = value => {
 if(value.type==="assistant" && (value.agent_id || value.parent_tool_use_id)) { const text=(value.message?.content || []).filter(part=>part.type==="text").map(part=>part.text).join("\n"); if(text) messages.set(value.agent_id || value.parent_tool_use_id,text); }
    for (const part of value.message?.content || []) {
      if(part.type==="tool_use" && ["Agent","Task"].includes(part.name) && part.input?.subagent_type) toolRoles.set(part.id,part.input.subagent_type);
    }
    const metadata=value.result?.thread;
    if(metadata?.agentRole) roles.set(metadata.id,metadata.agentRole);
    if (value.type === "system" && value.subtype === "task_started") {
      if (value.task_type) classified = true;
      if (value.task_type === "local_agent") agents.set(value.task_id || value.agent_id, {name: value.description,tool:value.tool_use_id});
    }
    if(value.type==="system" && value.subtype==="task_notification" && value.summary) reports.set(value.task_id || value.agent_id,value.summary);
    if (value.id === 4 && value.result?.thread?.id) roots.add(value.result.thread.id);
    const item = value.params?.item;
    if(value.method==="item/completed" && item?.type==="agentMessage" && value.params?.threadId) messages.set(value.params.threadId,item.text);
    if (item?.type === "subAgentActivity") {
      classified = true;
      if (["started", "completed", "failed", "interrupted"].includes(item.kind)) {
        const prior = agents.get(item.agentThreadId);
        agents.set(item.agentThreadId, {name: prior?.name || item.agentPath});
      }
    }
  };
  for (const event of events) {
    if (event.type !== "process.output" || event.encoding !== "base64") continue;
    try {
      const key = event.stream || "stdout";
      let state = streams.get(key);
      if (!state) { state = {decoder: new TextDecoder(), pending: ""}; streams.set(key, state); }
      const bytes = Uint8Array.from(atob(event.data), char => char.charCodeAt(0));
      state.pending += state.decoder.decode(bytes, {stream: true});
      let end;
      while ((end = state.pending.indexOf("\n")) >= 0) {
        const line = state.pending.slice(0, end); state.pending = state.pending.slice(end + 1);
        try { observe(JSON.parse(line)); } catch { /* Non-JSON output is diagnostic only. */ }
      }
      if (state.pending.length > 1 << 20) state.pending = "";
    } catch { /* Malformed raw output does not hide normalized activity. */ }
  }
  for (const state of streams.values()) { try { observe(JSON.parse(state.pending)); } catch { /* Partial live line. */ } }
  for(const [id,agent] of agents) agent.role=roles.get(id) || toolRoles.get(agent.tool);
 return {agents, roots, classified,reports,messages};
}

export function pollRunActivity(run) {
  if (["running", "queued"].includes(run?.state)) return true;
  // A cancelled run may still be uploading its terminal events. Bound polling
  // for old histories that predate stop acknowledgments.
  return run?.state === "cancelled" && run?.phase === "stopping";
}

export function agentLabel(child) {
  const reported = child.role || (child.name?.startsWith("/root/") ? child.name.split("/").at(-1) : "");
  return reported ? reported.replace(/[_-]+/g," ").replace(/^./,char=>char.toUpperCase()) : "Subagent";
}

// This is a bounded excerpt of the agent's own report, not a generated verdict.
export function agentOutcome(child) {
  let text=child.summary || "";
  try { const result=JSON.parse(text); if(typeof result.summary==="string") text=result.summary; } catch { /* Ordinary prose. */ }
  text=text.replace(/<[^>]*>/g,"").replace(/\[([^\]]+)\]\([^)]*\)/g,"$1").replace(/^[#>]+\s*/gm,"").replace(/[*`]/g,"").split(/\n\s*\n/).map(line=>line.trim()).find(Boolean) || "";
  text=text.replace(/^[-•]\s*/,"").replace(/\s+/g," ").trim();
  if(!text) return child.status==="running" ? "Working. No outcome yet." : "No outcome was reported.";
  if(text.length<=150) return text;
  const excerpt=text.slice(0,147);const boundary=excerpt.lastIndexOf(" ");return excerpt.slice(0,boundary>100?boundary:147)+"…";
}

export function agentInstanceLabels(activities) {
  const totals=new Map(), seen=new Map();
  for(const child of activities) {const label=agentLabel(child);totals.set(label,(totals.get(label)||0)+1);}
  return new Map(activities.map(child=>{const label=agentLabel(child),number=(seen.get(label)||0)+1;seen.set(label,number);return [child.native_id,totals.get(label)>1 ? `${label} ${number}` : label];}));
}
