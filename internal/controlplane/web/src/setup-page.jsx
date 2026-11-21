import React, { useCallback, useEffect, useState } from "react";
import { Check, CircleAlert, ExternalLink, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import { repoName, setupLine } from "./setup.js";

function CheckRow({ check }) {
  const tone = check.ok ? "pill-success" : check.required ? "pill-warning" : "";
  const Icon = check.ok ? Check : CircleAlert;
  return <li className="setup-check">
    <span className={`pill ${tone}`}><Icon className="size-3.5" aria-hidden="true" />{check.ok ? "Done" : check.required ? "Needed" : "Optional"}</span>
    <div>
      <strong>{check.name}</strong>
      <p className="ticket-meta">{check.detail}</p>
      {!check.ok && check.fix && <p className="setup-fix">{check.fix}{check.link && <> <a href={check.link} target="_blank" rel="noreferrer">Read how<ExternalLink className="size-3" aria-hidden="true" /></a></>}</p>}
      {check.ok && check.fix && check.link && <p className="ticket-meta"><a href={check.link} target="_blank" rel="noreferrer">{check.fix}</a></p>}
    </div>
  </li>;
}

// sentence starts a server message with a capital letter and ends it with a stop.
const sentence = text => text.charAt(0).toUpperCase() + text.slice(1).replace(/([^.])$/, "$1.");

function ProjectRow({ project, onRemove, busy }) {
  return <li className="project-row">
    <div>
      <strong>{project.repo}</strong>
      <p className="ticket-meta">{project.source === "config" ? "From config.yaml" : `Added by ${project.added_by}`}</p>
      {project.problem && <p className="form-error">{sentence(project.problem)}</p>}
    </div>
    {project.source === "app"
      ? <Button variant="ghost" size="sm" onClick={() => onRemove(project.repo)} disabled={busy} aria-label={`Remove ${project.repo}`}><Trash2 className="size-3.5" aria-hidden="true" />Remove</Button>
      : <span className="ticket-meta">Edit config.yaml to remove</span>}
  </li>;
}

export function SetupPage({ onSignedOut, onChange }) {
  const [setup, setSetup] = useState(null);
  const [error, setError] = useState("");
  const [repo, setRepo] = useState("");
  const [busy, setBusy] = useState(false);

  const handle = useCallback(async request => {
    setBusy(true);
    try {
      const next = await request();
      setSetup(next);
      setError("");
      onChange?.(next);
      return true;
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut();
      else setError(sentence(failure.message));
      return false;
    } finally {
      setBusy(false);
    }
  }, [onSignedOut, onChange]);

  useEffect(() => { handle(() => api("/api/v1/setup")); }, [handle]);

  async function add(event) {
    event.preventDefault();
    const name = repoName(repo);
    if (await handle(() => api("/api/v1/projects", { method: "POST", body: { repo: name } }))) setRepo("");
  }
  const remove = name => handle(() => api(`/api/v1/projects/${name.split("/").map(encodeURIComponent).join("/")}`, { method: "DELETE" }));
  const name = repoName(repo);

  return <div className="page setup-page">
    <header className="page-head">
      <div>
        <h1>Setup</h1>
        {setup && <p className="page-sub">{setupLine(setup)}</p>}
      </div>
    </header>
    {!setup && !error && <p className="page-note" role="status">Checking setup</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    {setup && <>
      <ul className="list setup-checks" aria-label="Checklist">{setup.checks.map(check => <CheckRow key={check.name} check={check} />)}</ul>
      <section className="setup-projects" aria-labelledby="projects-title">
        <div className="section-head">
          <h2 id="projects-title" className="chart-title">Projects</h2>
          {setup.install_url && <a className="ticket-meta" href={setup.install_url} target="_blank" rel="noreferrer">Install the GitHub App on more repositories<ExternalLink className="size-3" aria-hidden="true" /></a>}
        </div>
        <p className="page-note">A project is a repository Machinist can work on. {setup.install_url ? "The GitHub App must be installed on it first." : ""}</p>
        <form className="project-add" onSubmit={add}>
          <label className="field-label" htmlFor="project-repo">Repository</label>
          <div className="project-add-row">
            <input id="project-repo" className="field-control" value={repo} onChange={event => setRepo(event.target.value)} placeholder="owner/name or a GitHub link" autoComplete="off" />
            <Button type="submit" disabled={!name || busy}><Plus className="size-4" aria-hidden="true" />Add</Button>
          </div>
          {repo && !name && <p className="field-hint">Type it as owner/name, such as octocat/hello-world.</p>}
        </form>
        {setup.projects.length > 0
          ? <ul className="list">{setup.projects.map(project => <ProjectRow key={project.repo} project={project} onRemove={remove} busy={busy} />)}</ul>
          : <p className="empty">No projects yet. Add a repository above.</p>}
      </section>
    </>}
  </div>;
}
