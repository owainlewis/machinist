import React, { useState } from "react";
import { Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "./api.js";
import { automationOutcome } from "./automation-outcome.js";
import { formatTimestamp, State } from "./task-display.jsx";

export function AutomationsPage({ automations = [], loaded, error, onChanged }) {
  const [pending, setPending] = useState("");
  const [failure, setFailure] = useState("");
  async function run(automation) {
    setPending(automation.name);
    setFailure("");
    try {
      const ticket = await api(`/api/v1/automations/${encodeURIComponent(automation.name)}/run`, { method: "POST" });
      await onChanged?.();
      window.location.hash = `#/work/${encodeURIComponent(ticket.id)}`;
    } catch (requestError) {
      setFailure(requestError.message);
    } finally {
      setPending("");
    }
  }
  return <div className="page automations-page">
    <header className="page-head">
      <div>
        <h1>Automations</h1>
        <p className="page-sub">Jobs that start on a schedule.</p>
      </div>
    </header>
    {(error || failure) && <p className="form-error" role="alert">{error || failure}</p>}
    {!loaded && !error && <p className="page-note" role="status">Loading automations</p>}
    {loaded && !automations.length && <p className="empty">No automations yet. Add a file to <code>factory/automations</code>, then restart Machinist.</p>}
    {automations.length > 0 && <ul className="list">{automations.map(automation => {
      const outcome = automationOutcome(automation);
      return <li key={automation.name} className="automation">
        <div>
          <strong>{automation.name}</strong>
          <p className="ticket-meta">{automation.repo}, {automation.agent} agent</p>
          <p className="ticket-meta"><code>{automation.schedule}</code> in {automation.timezone}. Next <time dateTime={automation.next_due}>{formatTimestamp(automation.next_due)}</time></p>
        </div>
        <div className="automation-outcome">
          {automation.last_ticket && <State value={automation.last_ticket.state} />}
          <p>{outcome.text}</p>
          {outcome.link && <a href={outcome.link.href} target={outcome.link.external ? "_blank" : undefined} rel={outcome.link.external ? "noreferrer" : undefined}>{outcome.link.label}</a>}
        </div>
        <Button variant="outline" size="sm" disabled={Boolean(pending) || Boolean(outcome.active)} onClick={() => run(automation)}>
          <Play className="size-3.5" aria-hidden="true" />{pending === automation.name ? "Starting" : "Run now"}<span className="sr-only"> {automation.name}</span>
        </Button>
      </li>;
    })}</ul>}
  </div>;
}
