import React, { useCallback, useEffect, useRef, useState } from "react";
import { Check, ChevronRight, CircleAlert, ExternalLink, Plus, RefreshCw, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import { checkLink } from "./settings.js";
import { repoName, setupLine } from "./setup.js";

const refreshMillis = 10_000;

// sentence starts a server message with a capital letter and ends it with a stop.
export const sentence = text => text.charAt(0).toUpperCase() + text.slice(1).replace(/([^.])$/, "$1.");

// SettingsHead keeps the heading, subtitle line and right-side action geometry
// steady across cold skeleton loads, background refreshes, and resolved states.
export function SettingsHead({ title, sub, loading = false, refreshing = false, actionPlaceholder = null, children }) {
  const actions = loading ? actionPlaceholder : children;
  return <header className="page-head settings-head">
    <div className="settings-head-main">
      <div className="settings-head-title">
        <h1>{title}</h1>
        {refreshing && <span className="settings-refresh" role="status" aria-label={`Refreshing ${title}`} title="Refreshing"><RefreshCw className="size-3.5 state-working" aria-hidden="true" /></span>}
      </div>
      {sub
        ? <p className="page-sub">{sub}</p>
        : loading
          ? <p className="page-sub" aria-hidden="true"><span className="skeleton skeleton-sub" /></p>
          : <p className="page-sub settings-sub-spacer" aria-hidden="true">&nbsp;</p>}
    </div>
    {actions && <div className="settings-head-actions">{actions}</div>}
  </header>;
}

const overviewSkeletonRows = [
  ["76px", "64%"],
  ["70px", "54%"],
  ["84px", "72%"],
  ["68px", "48%"],
];

function OverviewSkeleton() {
  return <div className="settings-skeleton" role="status" aria-busy="true">
    <span className="sr-only">Checking setup</span>
    <ul className="list setup-checks" aria-hidden="true">
      {overviewSkeletonRows.map(([titleWidth, detailWidth], index) => <li key={index} className="setup-check">
        <span className="skeleton skeleton-pill" />
        <div className="skeleton-stack">
          <span className="skeleton skeleton-title" style={{ width: titleWidth }} />
          <span className="skeleton skeleton-text" style={{ width: detailWidth }} />
        </div>
        <span className="skeleton skeleton-icon" />
      </li>)}
    </ul>
  </div>;
}

function ProjectsSkeleton() {
  return <div className="settings-skeleton setup-projects" role="status" aria-busy="true">
    <span className="sr-only">Loading projects</span>
    <div className="skeleton-stack" aria-hidden="true">
      <span className="skeleton skeleton-text" style={{ width: "74%" }} />
    </div>
    <div className="project-add" aria-hidden="true">
      <span className="skeleton skeleton-label skeleton-field-label" />
      <div className="project-add-row">
        <span className="skeleton skeleton-input" />
        <span className="skeleton skeleton-button skeleton-add-button" />
      </div>
    </div>
    <ul className="list" aria-hidden="true">
      {[["180px", "132px"], ["144px", "116px"]].map(([titleWidth, metaWidth], index) => <li key={index} className="project-row">
        <div className="skeleton-stack skeleton-grow">
          <span className="skeleton skeleton-title" style={{ width: titleWidth }} />
          <span className="skeleton skeleton-text" style={{ width: metaWidth }} />
        </div>
        <span className="skeleton skeleton-button-sm" />
      </li>)}
    </ul>
  </div>;
}

function CheckRow({ check }) {
  const tone = check.ok ? "pill-success" : (check.required || check.warning) ? "pill-warning" : "";
  const Icon = check.ok ? Check : CircleAlert;
  const [href, label] = checkLink(check.name);
  const badge = check.ok ? "Done" : check.required ? "Needed" : check.warning ? "Out of date" : "Optional";
  return <li className="setup-check">
    <span className={`pill ${tone}`}><Icon className="size-3.5" aria-hidden="true" />{badge}</span>
    <div>
      <strong>{check.name}</strong>
      <p className="ticket-meta">{check.detail}</p>
      {!check.ok && check.fix && <p className="setup-fix">{check.fix}{check.link && <> <a href={check.link} target="_blank" rel="noreferrer">Read how<ExternalLink className="size-3" aria-hidden="true" /></a></>}</p>}
      {check.ok && check.fix && check.link && <p className="ticket-meta"><a href={check.link} target="_blank" rel="noreferrer">{check.fix}</a></p>}
    </div>
    {href && <a className="setup-check-open" href={href} aria-label={label}><ChevronRight className="size-4" aria-hidden="true" /></a>}
  </li>;
}

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

// useSetup loads the checklist and the projects. Runners start and the app
// gets installed outside the app, so it checks again every few seconds while
// a page is open, and when the window comes back into focus.
function useSetup({ initialSetup = null, onSignedOut, onChange }) {
  const [setup, setSetup] = useState(initialSetup);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const version = useRef(0);

  useEffect(() => {
    if (initialSetup) setSetup(current => current ?? initialSetup);
  }, [initialSetup]);

  const handle = useCallback(async request => {
    setBusy(true);
    try {
      const next = await request();
      version.current += 1;
      setSetup(next);
      setError("");
      setRefreshing(false);
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

  useEffect(() => {
    let cancelled = false;
    const requestVersion = version.current;
    setRefreshing(true);
    api("/api/v1/setup")
      .then(next => {
        if (cancelled || requestVersion !== version.current) return;
        setSetup(next);
        setError("");
        onChange?.(next);
      })
      .catch(failure => {
        if (cancelled || requestVersion !== version.current) return;
        if (failure instanceof SignedOutError) onSignedOut();
        else setError(sentence(failure.message));
      })
      .finally(() => {
        if (!cancelled && requestVersion === version.current) setRefreshing(false);
      });
    const timer = window.setInterval(refresh, refreshMillis);
    window.addEventListener("focus", refresh);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      window.removeEventListener("focus", refresh);
    };
    async function refresh() {
      const refreshVersion = version.current;
      try {
        const next = await api("/api/v1/setup");
        if (cancelled || refreshVersion !== version.current) return;
        setSetup(next);
        setError("");
        onChange?.(next);
      } catch (failure) {
        if (cancelled || refreshVersion !== version.current) return;
        if (failure instanceof SignedOutError) onSignedOut();
      }
    }
  }, [onChange, onSignedOut]);
  return { setup, error, busy, refreshing, handle };
}

// OverviewPage is the setup checklist. A new install opens here until every
// required step is done.
export function OverviewPage({ setup: initialSetup = null, onSignedOut, onChange }) {
  const { setup, error, refreshing } = useSetup({ initialSetup, onSignedOut, onChange });
  const loading = !setup && !error;
  return <>
    <SettingsHead title="Overview" sub={setup && setupLine(setup)} loading={loading} refreshing={Boolean(setup && refreshing)} />
    {error && <p className="form-error" role="alert">{error}</p>}
    {loading && <OverviewSkeleton />}
    {setup && <ul className="list setup-checks" aria-label="Checklist">{setup.checks.map(check => <CheckRow key={check.name} check={check} />)}</ul>}
  </>;
}

// ProjectsPage lists the repositories Machinist works on, and adds and removes them.
export function ProjectsPage({ setup: initialSetup = null, onSignedOut, onChange }) {
  const { setup, error, busy, refreshing, handle } = useSetup({ initialSetup, onSignedOut, onChange });
  const [repo, setRepo] = useState("");

  async function add(event) {
    event.preventDefault();
    if (await handle(() => api("/api/v1/projects", { method: "POST", body: { repo: repoName(repo) } }))) setRepo("");
  }
  const remove = name => handle(() => api(`/api/v1/projects/${name.split("/").map(encodeURIComponent).join("/")}`, { method: "DELETE" }));
  const name = repoName(repo);
  const count = setup?.projects.length || 0;
  const loading = !setup && !error;

  return <>
    <SettingsHead
      title="Projects"
      sub={setup && (count ? `${count} ${count === 1 ? "repository" : "repositories"} connected.` : "No repository is connected yet.")}
      loading={loading}
      refreshing={Boolean(setup && refreshing)}
    >
      {setup?.install_url && <a className="settings-link" href={setup.install_url} target="_blank" rel="noreferrer">Install the GitHub App on more repositories<ExternalLink className="size-3" aria-hidden="true" /></a>}
    </SettingsHead>
    {error && !setup && <p className="form-error" role="alert">{error}</p>}
    {loading && <ProjectsSkeleton />}
    {setup && <section className="setup-projects" aria-label="Projects">
      <p className="page-note">A project is a repository Machinist can work on. {setup.install_url ? "The GitHub App must be installed on it first." : ""}</p>
      <form className="project-add" onSubmit={add}>
        <label className="field-label" htmlFor="project-repo">Repository</label>
        <div className="project-add-row">
          <input id="project-repo" className="field-control" value={repo} onChange={event => setRepo(event.target.value)} placeholder="owner/name or a GitHub link" autoComplete="off" />
          <Button type="submit" disabled={!name || busy}><Plus className="size-4" aria-hidden="true" />Add</Button>
        </div>
        {repo && !name && <p className="field-hint">Type it as owner/name, such as octocat/hello-world.</p>}
      </form>
      {error && <p className="form-error" role="alert">{error}</p>}
      {count > 0
        ? <ul className="list">{setup.projects.map(project => <ProjectRow key={project.repo} project={project} onRemove={remove} busy={busy} />)}</ul>
        : <p className="empty">No projects yet. Add a repository above.</p>}
    </section>}
  </>;
}
