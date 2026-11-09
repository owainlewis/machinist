import React, { useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { api } from "./api.js";

const maxTitle = 100;

// One box takes an issue number, an issue link, or the work written out.
export function assignRequest(text, repository, requestID) {
  const value = text.trim();
  const number = value.match(/^#?(\d+)$/);
  if (number) return { request_id: requestID, repo: repository, issue_number: Number(number[1]) };
  const link = value.match(/^https:\/\/github\.com\/([^/\s]+\/[^/\s]+)\/issues\/(\d+)\/?$/);
  if (link) return { request_id: requestID, repo: link[1], issue_number: Number(link[2]) };
  const title = value.split("\n")[0].replace(/^#+\s*/, "").slice(0, maxTitle);
  return { request_id: requestID, repo: repository, title, body: value };
}

export function AssignPage({ repositories = [] }) {
  const [text, setText] = useState("");
  const [choice, setChoice] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const attempt = useRef(null);
  const repository = repositories.includes(choice) ? choice : repositories[0] || "";

  async function submit(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    // A retry after a lost response keeps its ID, so it cannot make a second ticket.
    const fingerprint = `${repository}\n${text.trim()}`;
    if (attempt.current?.fingerprint !== fingerprint) attempt.current = { fingerprint, id: crypto.randomUUID() };
    try {
      const ticket = await api("/api/v1/tickets", { method: "POST", body: assignRequest(text, repository, attempt.current.id) });
      attempt.current = null;
      setText("");
      window.location.hash = `#/work/${encodeURIComponent(ticket.id)}`;
    } catch (failure) {
      setError(failure.message);
    } finally {
      setPending(false);
    }
  }

  if (!repositories.length) return <div className="page assign-page">
    <h1>Assign work</h1>
    <p className="page-note">Connect a repository first. Add it to <code>repositories</code> in config.toml, then restart Machinist.</p>
  </div>;

  return <form className="page assign-page" onSubmit={submit}>
    <h1><label htmlFor="assign-work">What needs doing?</label></h1>
    <textarea id="assign-work" className="assign-box" autoFocus value={text} onChange={event => setText(event.target.value)} placeholder="Describe the work, or paste an issue number or link" />
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="assign-actions">
      <label className="sr-only" htmlFor="assign-repository">Repository</label>
      <select id="assign-repository" className="field-control" value={repository} onChange={event => setChoice(event.target.value)}>
        {repositories.map(name => <option key={name} value={name}>{name}</option>)}
      </select>
      <Button type="submit" disabled={pending || !text.trim()}>{pending ? "Assigning" : "Assign"}</Button>
    </div>
  </form>;
}
