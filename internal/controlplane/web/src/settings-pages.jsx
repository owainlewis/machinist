import React, { Fragment, useCallback, useEffect, useState } from "react";
import { CircleAlert, ExternalLink, ShieldCheck, ShieldOff, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import { AgentBadges } from "./agent-badge.jsx";
import { agentSummary, harnessLabel } from "./agents.js";
import { relativeTime } from "./display.jsx";
import { HarnessMark } from "./harness-mark.jsx";
import { dayLabel, factoryUpdateLine, githubLine, pollLabel, roleLabel, tokenLine } from "./settings.js";
import { sentence } from "./setup-page.jsx";

// useResource loads what a page shows from one or more API routes.
function useResource(paths, onSignedOut) {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const key = paths.join(" ");
  const load = useCallback(async () => {
    try {
      const found = await Promise.all(key.split(" ").map(path => api(path)));
      setData(found);
      setError("");
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut();
      else setError(sentence(failure.message));
    }
  }, [key, onSignedOut]);
  useEffect(() => { load(); }, [load]);
  return { data, error, setData };
}

function Head({ title, sub, children }) {
  return <header className="page-head">
    <div>
      <h1>{title}</h1>
      {sub && <p className="page-sub">{sub}</p>}
    </div>
    {children}
  </header>;
}

function Status({ data, error, label }) {
  if (error) return <p className="form-error" role="alert">{error}</p>;
  if (!data) return <p className="page-note" role="status">{label}</p>;
  return null;
}

function OnOff({ on, labels = ["On", "Off"] }) {
  return <span className={`pill ${on ? "pill-success" : ""}`}>{on ? labels[0] : labels[1]}</span>;
}

// Facts is a list of settings, a name beside each value.
function Facts({ label, children }) {
  return <dl className="list settings-facts" aria-label={label}>{children}</dl>;
}

function Fact({ name, children, hint }) {
  return <div className="settings-fact">
    <dt>{name}</dt>
    <dd>{children}{hint && <p className="ticket-meta">{hint}</p>}</dd>
  </div>;
}

function DocsLink({ href, children }) {
  return <a className="settings-link" href={href} target="_blank" rel="noreferrer">{children}<ExternalLink className="size-3" aria-hidden="true" /></a>;
}

const docs = "https://github.com/owainlewis/machinist/blob/main/docs/";

export function GitHubPage({ onSignedOut }) {
  const { data, error } = useResource(["/api/v1/settings/github"], onSignedOut);
  const settings = data?.[0];
  return <>
    <Head title="GitHub" sub={settings && githubLine(settings)} />
    <Status data={data} error={error} label="Asking GitHub" />
    {settings && <>
      <Facts label="GitHub settings">
        <Fact name="Handle" hint="github.handle: the login that owns the token. Jobs commit and comment under it."><code>{settings.handle}</code></Fact>
        {settings.app
          ? <Fact name="GitHub App" hint={settings.app.error ? "" : `github.app.id ${settings.app.id}. The private key stays in its file.`}>
            {settings.app.error
              ? <span className="form-error">{sentence(`Machinist cannot reach the GitHub App: ${settings.app.error}`)}</span>
              : <span className="settings-inline">{settings.app.name}{settings.app.install_url && <DocsLink href={settings.app.install_url}>Install on more repositories</DocsLink>}</span>}
          </Fact>
          : <Fact name="GitHub App" hint="Set github.app in config.yaml so each run gets a token for its repository alone, and commits as the app.">
            <span className="settings-inline"><OnOff on={false} labels={["Set", "Not set"]} /><DocsLink href={`${docs}github-app.md`}>Read how</DocsLink></span>
          </Fact>}
        <Fact name="Auto-merge" hint={settings.auto_merge ? "github.auto_merge: a job merges its pull request once checks pass and its review is clean." : "github.auto_merge is false: a person merges every pull request."}>
          <OnOff on={settings.auto_merge} />
        </Fact>
      </Facts>
      <section className="settings-section" aria-labelledby="branches-title">
        <h2 id="branches-title" className="chart-title">Default branches</h2>
        <p className="page-note">Branch protection is what stops a pull request merging before its checks pass. Machinist does not change it.</p>
        {settings.repositories.length
          ? <ul className="list">{settings.repositories.map(repo => <BranchRow key={repo.repo} repo={repo} />)}</ul>
          : <p className="empty">No projects yet. Add one on the <a href="#/settings/projects">Projects</a> page.</p>}
      </section>
    </>}
  </>;
}

function BranchRow({ repo }) {
  const Icon = repo.protected ? ShieldCheck : ShieldOff;
  return <li className="settings-row">
    <div className="settings-row-main">
      <strong>{repo.repo}</strong>
      {repo.default_branch && <span className="ticket-meta"><code>{repo.default_branch}</code></span>}
    </div>
    {!repo.problem && <span className={`pill ${repo.protected ? "pill-success" : "pill-warning"}`}><Icon className="size-3.5" aria-hidden="true" />{repo.protected ? "Protected" : "Not protected"}</span>}
    {(repo.problem || repo.warnings.length > 0) && <ul className="settings-warnings">
      {repo.problem && <li className="form-error">{sentence(repo.problem)}</li>}
      {repo.warnings.map(warning => <li key={warning}><CircleAlert className="size-3.5" aria-hidden="true" />{warning}</li>)}
    </ul>}
  </li>;
}

export function LinearPage({ onSignedOut }) {
  const { data, error } = useResource(["/api/v1/settings/linear"], onSignedOut);
  const linear = data?.[0];
  return <>
    <Head title="Linear" sub={linear && (linear.enabled ? "Issues assigned to the Machinist app start jobs." : "Off. Jobs start from the web app, the API and schedules.")}>
      {linear && <OnOff on={linear.enabled} />}
    </Head>
    <Status data={data} error={error} label="Loading Linear" />
    {linear && !linear.enabled && <section className="panel settings-howto" aria-labelledby="linear-howto">
      <h2 id="linear-howto" className="chart-title">Turn on Linear</h2>
      <ol>
        <li>Make a Linear app for Machinist with client credentials on, and note its user ID.</li>
        <li>Set <EnvNames names={linear.credential_env} /> where the control plane runs. Machinist gets and renews its own access token. They never go in config.yaml.</li>
        <li>In config.yaml, add a <code>linear</code> section with <code>app_user_id</code>, <code>allowed_users</code> and <code>teams</code>, which maps each team key to a repository.</li>
        <li>Restart the control plane, and assign a test issue to the app.</li>
      </ol>
      <DocsLink href={linear.docs}>Read the setup guide</DocsLink>
    </section>}
    {linear?.enabled && <>
      <Facts label="Linear settings">
        <Fact name="App user" hint="Assigning an issue to this Linear user starts a job."><code>{linear.app_user_id}</code></Fact>
        <Fact name="Allowed users" hint="Only their assignments start jobs.">
          <ul className="settings-values">{linear.allowed_users.map(user => <li key={user}><code>{user}</code></li>)}</ul>
        </Fact>
        <Fact name="Preset"><code>{linear.preset}</code></Fact>
        <Fact name="Poll interval">{pollLabel(linear.poll_seconds)}</Fact>
        <Fact name="Limit">{linear.max_jobs_per_hour} jobs an hour</Fact>
        <Fact name="Sign-in" hint={linear.renews ? "Machinist gets and renews its own access token. The secret is never shown." : "A fixed access token, read from the environment. It is never shown."}><EnvNames names={linear.credential_env} /></Fact>
      </Facts>
      <section className="settings-section" aria-labelledby="teams-title">
        <h2 id="teams-title" className="chart-title">Teams</h2>
        <p className="page-note">An issue works in its team's repository. Issues from other teams are ignored.</p>
        {linear.teams.length
          ? <ul className="list">{linear.teams.map(team => <li key={team.team} className="settings-row settings-map"><strong>{team.team}</strong><span aria-hidden="true">→</span><span>{team.repo}</span></li>)}</ul>
          : <p className="empty">No teams are mapped. Add <code>teams</code> to the <code>linear</code> section of config.yaml, with each team key and its repository.</p>}
      </section>
    </>}
  </>;
}

function EnvNames({ names }) {
  return names.map((name, index) => <Fragment key={name}>{index > 0 && " and "}<code>{name}</code></Fragment>);
}

export function TokensPage({ onSignedOut }) {
  const { data, error, setData } = useResource(["/api/v1/tokens"], onSignedOut);
  const [confirming, setConfirming] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const tokens = data?.[0].tokens;

  async function revoke(token) {
    setBusy(true);
    try {
      const next = await api(`/api/v1/tokens/${encodeURIComponent(token.name)}`, { method: "DELETE" });
      if (!next) { onSignedOut(); return; }
      setData([next]);
      setFailure("");
      setConfirming("");
    } catch (problem) {
      if (problem instanceof SignedOutError) onSignedOut();
      else setFailure(sentence(problem.message));
    } finally {
      setBusy(false);
    }
  }

  return <>
    <Head title="Tokens" sub={tokens && tokenLine(tokens)} />
    <p className="page-note">People and runners sign in with tokens. Make one with <code>machinist token create &lt;name&gt;</code>. Only its hash is kept, so a token is shown once, when it is made.</p>
    <Status data={data} error={error} label="Loading tokens" />
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {tokens && (tokens.length
      ? <ul className="list" aria-label="Tokens">{tokens.map(token => <TokenRow key={`${token.name}-${token.created_at}`} token={token} busy={busy} confirming={confirming === token.name && !token.revoked_at} onAsk={() => setConfirming(token.name)} onCancel={() => setConfirming("")} onRevoke={() => revoke(token)} />)}</ul>
      : <p className="empty">No tokens yet.</p>)}
  </>;
}

function TokenRow({ token, busy, confirming, onAsk, onCancel, onRevoke }) {
  const [role, about] = roleLabel(token.role);
  const revoked = Boolean(token.revoked_at);
  return <li className="settings-row token-row" data-revoked={revoked || undefined}>
    <div className="settings-row-main">
      <strong>{token.name}</strong>
      {token.current && <span className="pill pill-info">You</span>}
      <span className="pill" title={about}>{role}</span>
    </div>
    <dl className="token-times">
      <div><dt>Created</dt><dd><time dateTime={token.created_at}>{dayLabel(token.created_at)}</time></dd></div>
      <div><dt>Last used</dt><dd>{token.last_used_at ? <time dateTime={token.last_used_at}>{relativeTime(token.last_used_at)}</time> : "Never"}</dd></div>
      {revoked && <div><dt>Revoked</dt><dd><time dateTime={token.revoked_at}>{dayLabel(token.revoked_at)}</time></dd></div>}
    </dl>
    <div className="token-actions">
      {revoked && <span className="pill pill-danger">Revoked</span>}
      {!revoked && !confirming && <Button variant="ghost" size="sm" onClick={onAsk} disabled={busy} aria-label={`Revoke ${token.name}`}><Trash2 className="size-3.5" aria-hidden="true" />Revoke</Button>}
      {confirming && <span className="token-confirm" role="group" aria-label={`Revoke ${token.name}?`}>
        <span className="ticket-meta">{token.current ? "This signs you out." : token.role === "runner" ? "Its runner stops." : "Its sign-ins end."}</span>
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={busy}>Keep</Button>
        <Button variant="outline" size="sm" className="text-danger" onClick={onRevoke} disabled={busy}>Revoke</Button>
      </span>}
    </div>
  </li>;
}

export function PresetsPage({ onSignedOut }) {
  const { data, error } = useResource(["/api/v1/presets", "/api/v1/agents"], onSignedOut);
  const [presets, agents] = data || [];
  const updates = presets?.updates || [];
  return <>
    <Head title="Presets and agents" sub={data && `${presets.presets.length} ${presets.presets.length === 1 ? "preset" : "presets"} and ${agents.agents.length} ${agents.agents.length === 1 ? "agent" : "agents"}.`} />
    <p className="page-note">These come from <code>factory/presets</code> and <code>factory/agents</code>. Change them in git and restart the control plane.</p>
    {updates.length > 0 && <div className="page-warning factory-warning" role="status">
      <p><strong>{factoryUpdateLine(updates)}</strong> Run <code>machinist factory upgrade</code> and restart the control plane.</p>
      <ul className="factory-warning-files">
        {updates.map(file => <li key={file.path}>
          <code>{file.path}</code>
          {file.status === "edited" && " (edited locally)"}
          {file.status === "missing" && " (missing)"}
        </li>)}
      </ul>
    </div>}
    <Status data={data} error={error} label="Loading presets" />
    {data && <>
      <section className="settings-section" aria-labelledby="presets-title">
        <h2 id="presets-title" className="chart-title">Presets</h2>
        <ul className="list">{presets.presets.map(preset => <li key={preset.name} className="settings-row preset-row">
          <div className="settings-row-main">
            <strong>{preset.name}</strong>
            {preset.name === presets.default && <span className="pill pill-info">Default</span>}
          </div>
          {preset.description && <p className="ticket-meta">{preset.description}</p>}
          <ol className="preset-step-list" aria-label={`Steps of ${preset.name}`}>{preset.steps.map((step, index) => <li key={`${step.name}-${index}`}>
            <span className="preset-step-number">{index + 1}</span>
            <span className="step-name">{step.name}</span>
            <AgentBadges agents={step.agents} step={step.name} />
          </li>)}</ol>
        </li>)}</ul>
      </section>
      <section className="settings-section" aria-labelledby="agents-title">
        <h2 id="agents-title" className="chart-title">Agents</h2>
        {agents.agents.length
          ? <ul className="list">{agents.agents.map(agent => <li key={agent.name} className="settings-row agent-row">
            <div className="settings-row-main">
              <HarnessMark harness={agent.harness} className="size-4" />
              <strong>{agent.name}</strong>
            </div>
            <dl className="token-times">
              <div><dt>Harness</dt><dd>{harnessLabel(agent.harness)}</dd></div>
              <div><dt>Model</dt><dd>{agent.model ? <code>{agent.model}</code> : agentSummary(agent)}</dd></div>
              <div><dt>Used by</dt><dd>{agent.presets.length ? agent.presets.join(", ") : "No preset"}</dd></div>
            </dl>
          </li>)}</ul>
          : <p className="empty">No agents. A step runs one with <code>machinist agent &lt;name&gt;</code>.</p>}
      </section>
    </>}
  </>;
}
