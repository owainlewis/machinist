import React, { useRef, useState } from "react";
import { ArrowUp } from "lucide-react";
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

// AssignBox is the composer at the top of Home.
export function AssignBox({ repositories = [] }) {
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

  if (!repositories.length) return <p className="page-note">Connect a repository first. Add it to <code>repositories</code> in config.toml, then restart Machinist.</p>;

  function pressKey(event) {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    if (text.trim() && !pending) event.currentTarget.form.requestSubmit();
  }

  return <form className="assign-box" onSubmit={submit}>
    <div className="chat-composer">
      <textarea id="assign-work" className="chat-input assign-input" autoFocus value={text} onChange={event => setText(event.target.value)} onKeyDown={pressKey} placeholder="Describe the work, or paste an issue number or link" />
      <div className="chat-composer-row">
        <label className="sr-only" htmlFor="assign-repository">Repository</label>
        <select id="assign-repository" className="assign-repository" value={repository} onChange={event => setChoice(event.target.value)}>
          {repositories.map(name => <option key={name} value={name}>{name}</option>)}
        </select>
        <button type="submit" className="chat-send" aria-label={pending ? "Assigning" : "Assign"} disabled={pending || !text.trim()}><ArrowUp className="size-4" aria-hidden="true" /></button>
      </div>
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
  </form>;
}
