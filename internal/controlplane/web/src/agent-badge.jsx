import React from "react";
import { HarnessMark } from "./harness-mark.jsx";
import { agentSummary, agentTitle } from "./agents.js";

// AgentBadge names an agent and what runs it: the harness's mark, the agent's
// name and its model. Inside a step of the same name, the name is left out.
export function AgentBadge({ agent, step = "" }) {
  return <span className="agent-badge" data-harness={agent.harness} title={agentTitle(agent)}>
    <HarnessMark harness={agent.harness} className="agent-badge-mark" />
    {agent.name !== step && <span className="agent-badge-name">{agent.name}</span>}
    <span className="agent-badge-model">{agentSummary(agent)}</span>
    <span className="sr-only">{agentTitle(agent)}</span>
  </span>;
}

// AgentBadges lists the agents a step runs. A step that runs none shows nothing.
export function AgentBadges({ agents = [], step = "", className = "" }) {
  if (!agents?.length) return null;
  return <span className={`agent-badges ${className}`.trim()}>{agents.map((agent, index) => <AgentBadge key={`${agent.name}-${index}`} agent={agent} step={step} />)}</span>;
}
