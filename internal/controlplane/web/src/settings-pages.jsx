import React, { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { Check, ChevronRight, CircleAlert, Copy, ExternalLink, Plus, RefreshCw, ShieldCheck, ShieldOff, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { api, SignedOutError } from "./api.js";
import { AgentBadges } from "./agent-badge.jsx";
import { agentSummary, harnessLabel } from "./agents.js";
import { relativeTime } from "./display.jsx";
import { HarnessMark } from "./harness-mark.jsx";
import { dayLabel, factoryUpdateLine, githubLine, pollLabel, roleLabel, tokenLine, tokenPrefixLabel, validateTokenName } from "./settings.js";
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
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [role, setRole] = useState("operator");
  const [revealed, setRevealed] = useState(null);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");
  const [confirming, setConfirming] = useState(null);
  const [showRevoked, setShowRevoked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const copyTimer = useRef(null);
  const tokens = data?.[0].tokens;
  const activeTokens = tokens ? tokens.filter(token => !token.revoked_at) : null;
  const revokedTokens = tokens ? tokens.filter(token => Boolean(token.revoked_at)) : [];
  const nameCheck = validateTokenName(name);

  useEffect(() => () => { window.clearTimeout(copyTimer.current); }, []);

  function openCreate() {
    setCreating(true);
    setConfirming(null);
    setFailure("");
  }

  function closeCreate() {
    setCreating(false);
    setName("");
    setRole("operator");
    setFailure("");
  }

  function dismissRevealed() {
    window.clearTimeout(copyTimer.current);
    setRevealed(null);
    setCopied(false);
    setCopyError("");
  }

  async function copyToken() {
    if (!revealed) return;
    setCopyError("");
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(revealed.token);
      } else {
        const field = document.createElement("textarea");
        field.value = revealed.token;
        field.setAttribute("readonly", "");
        field.style.position = "fixed";
        field.style.opacity = "0";
        document.body.appendChild(field);
        field.select();
        const ok = document.execCommand?.("copy");
        document.body.removeChild(field);
        if (!ok) throw new Error("clipboard unavailable");
      }
      setCopied(true);
      window.clearTimeout(copyTimer.current);
      copyTimer.current = window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
      setCopyError("Could not copy automatically. Select the token and copy it.");
    }
  }

  async function create(event) {
    event.preventDefault();
    if (!nameCheck.name || nameCheck.error || busy) return;
    setBusy(true);
    try {
      const created = await api("/api/v1/tokens", { method: "POST", body: { name: nameCheck.name, role } });
      setData([{ tokens: created.tokens }]);
      window.clearTimeout(copyTimer.current);
      setRevealed({ token: created.token, name: created.name, role: created.role, action: "created" });
      setCopied(false);
      setCopyError("");
      setCreating(false);
      setName("");
      setRole("operator");
      setConfirming(null);
      setFailure("");
    } catch (problem) {
      if (problem instanceof SignedOutError) onSignedOut();
      else setFailure(sentence(problem.message));
    } finally {
      setBusy(false);
    }
  }

  async function regenerate(token) {
    setBusy(true);
    try {
      const next = await api(`/api/v1/tokens/${encodeURIComponent(token.name)}/regenerate`, { method: "POST" });
      setData([{ tokens: next.tokens }]);
      window.clearTimeout(copyTimer.current);
      setRevealed({ token: next.token, name: next.name, role: next.role, action: "regenerated" });
      setCopied(false);
      setCopyError("");
      setConfirming(null);
      setFailure("");
    } catch (problem) {
      if (problem instanceof SignedOutError) onSignedOut();
      else setFailure(sentence(problem.message));
    } finally {
      setBusy(false);
    }
  }

  async function revoke(token) {
    setBusy(true);
    try {
      const next = await api(`/api/v1/tokens/${encodeURIComponent(token.name)}`, { method: "DELETE" });
      if (!next) { onSignedOut(); return; }
      setData([next]);
      if (revealed?.name === token.name) dismissRevealed();
      setFailure("");
      setConfirming(null);
    } catch (problem) {
      if (problem instanceof SignedOutError) onSignedOut();
      else setFailure(sentence(problem.message));
    } finally {
      setBusy(false);
    }
  }

  return <>
    <Head title="Tokens" sub={tokens && tokenLine(tokens)}>
      {tokens && !creating && <Button size="sm" onClick={openCreate} disabled={busy}><Plus className="size-4" aria-hidden="true" />New token</Button>}
    </Head>
    <p className="page-note">People and runners sign in with tokens. Only a token's hash and short prefix are kept, so each full token is shown once, when it is made.</p>
    {creating && <form className="panel token-form" aria-label="New token" onSubmit={create}>
      <div className="token-form-fields">
        <div>
          <label className="field-label" htmlFor="token-name">Name</label>
          <input id="token-name" className="field-control" value={name} onChange={event => setName(event.target.value)} placeholder="e.g. owain or mac-mini" autoComplete="off" autoFocus />
          {nameCheck.error && <p className="form-error">{nameCheck.error}</p>}
        </div>
        <div>
          <label className="field-label" htmlFor="token-role">Role</label>
          <Select id="token-role" value={role} onChange={setRole}>
            <option value="operator">Person</option>
            <option value="runner">Runner</option>
          </Select>
          <p className="ticket-meta token-role-hint">{roleLabel(role)[1]}</p>
        </div>
      </div>
      <div className="token-form-actions">
        <Button type="submit" size="sm" disabled={!nameCheck.name || Boolean(nameCheck.error) || busy}>{busy ? "Creating" : "Create token"}</Button>
        <Button type="button" variant="ghost" size="sm" onClick={closeCreate} disabled={busy}>Cancel</Button>
      </div>
    </form>}
    {revealed && <section className="panel token-reveal" aria-label="New token value">
      <div className="token-reveal-head">
        <div>
          <strong>{revealed.action === "regenerated" ? `Regenerated token for ${revealed.name}` : `Token created for ${revealed.name}`}</strong>
          <p className="ticket-meta">Copy this token now. It won't be shown again.</p>
        </div>
        <Button type="button" variant="ghost" size="sm" onClick={dismissRevealed} aria-label="Dismiss token"><X className="size-3.5" aria-hidden="true" />Done</Button>
      </div>
      <div className="token-reveal-value">
        <code className="token-secret">{revealed.token}</code>
        <Button type="button" variant="outline" size="sm" onClick={copyToken}>
          {copied ? <><Check className="size-3.5" aria-hidden="true" />Copied</> : <><Copy className="size-3.5" aria-hidden="true" />Copy</>}
        </Button>
      </div>
      {copyError && <p className="form-error" role="alert">{copyError}</p>}
    </section>}
    <Status data={data} error={error} label="Loading tokens" />
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {activeTokens && (activeTokens.length
      ? <ul className="list" aria-label="Tokens">{activeTokens.map(token => <TokenRow key={`${token.name}-${token.created_at}`} token={token} busy={busy} confirming={confirming?.name === token.name ? confirming.kind : ""} onAskRegenerate={() => setConfirming({ kind: "regenerate", name: token.name })} onAskRevoke={() => setConfirming({ kind: "revoke", name: token.name })} onCancel={() => setConfirming(null)} onRegenerate={() => regenerate(token)} onRevoke={() => revoke(token)} />)}</ul>
      : <p className="empty">No active tokens.</p>)}
    {revokedTokens.length > 0 && <div className="token-revoked-section">
      <Button type="button" variant="ghost" size="sm" className="token-revoked-toggle" aria-expanded={showRevoked} onClick={() => setShowRevoked(open => !open)}>
        <ChevronRight className={cn("size-3.5 token-revoked-chevron", showRevoked && "token-revoked-chevron-open")} aria-hidden="true" />
        {showRevoked ? `Hide revoked (${revokedTokens.length})` : `Show revoked (${revokedTokens.length})`}
      </Button>
      {showRevoked && <ul className="list" aria-label="Revoked tokens">{revokedTokens.map((token, index) => <TokenRow key={`${token.name}-${token.created_at}-${index}`} token={token} />)}</ul>}
    </div>}
  </>;
}

function TokenRow({ token, busy, confirming, onAskRegenerate, onAskRevoke, onCancel, onRegenerate, onRevoke }) {
  const [role, about] = roleLabel(token.role);
  const revoked = Boolean(token.revoked_at);
  return <li className="settings-row token-row" data-revoked={revoked || undefined}>
    <div className="settings-row-main">
      <strong>{token.name}</strong>
      {token.current && <span className="pill pill-info">You</span>}
      <span className="pill" title={about}>{role}</span>
    </div>
    <div className="token-actions">
      {revoked && <span className="pill pill-danger">Revoked</span>}
      {!revoked && !confirming && <>
        <Button variant="ghost" size="sm" onClick={onAskRegenerate} disabled={busy} aria-label={`Regenerate ${token.name}`}><RefreshCw className="size-3.5" aria-hidden="true" />Regenerate</Button>
        <Button variant="ghost" size="sm" onClick={onAskRevoke} disabled={busy} aria-label={`Revoke ${token.name}`}><Trash2 className="size-3.5" aria-hidden="true" />Revoke</Button>
      </>}
      {confirming === "regenerate" && <span className="token-confirm" role="group" aria-label={`Regenerate ${token.name}?`}>
        <span className="ticket-meta">{token.current ? "Your old token stops working. This browser stays signed in." : token.role === "runner" ? "Its old token stops working; update the runner." : "Its old token stops working."}</span>
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={busy}>Cancel</Button>
        <Button variant="outline" size="sm" onClick={onRegenerate} disabled={busy}>Regenerate</Button>
      </span>}
      {confirming === "revoke" && <span className="token-confirm" role="group" aria-label={`Revoke ${token.name}?`}>
        <span className="ticket-meta">{token.current ? "This signs you out." : token.role === "runner" ? "Its runner stops." : "Its sign-ins end."}</span>
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={busy}>Keep</Button>
        <Button variant="outline" size="sm" className="text-danger" onClick={onRevoke} disabled={busy}>Revoke</Button>
      </span>}
    </div>
    <dl className="token-times">
      <div><dt>Prefix</dt><dd>{token.prefix ? <code className="token-prefix">{tokenPrefixLabel(token.prefix)}</code> : <span className="token-no-prefix">No prefix</span>}</dd></div>
      <div><dt>Created</dt><dd><time dateTime={token.created_at}>{dayLabel(token.created_at)}</time></dd></div>
      <div><dt>Last used</dt><dd>{token.last_used_at ? <time dateTime={token.last_used_at}>{relativeTime(token.last_used_at)}</time> : "Never"}</dd></div>
      {revoked && <div><dt>Revoked</dt><dd><time dateTime={token.revoked_at}>{dayLabel(token.revoked_at)}</time></dd></div>}
    </dl>
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
