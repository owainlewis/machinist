import React, { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { Check, ChevronRight, CircleAlert, Copy, ExternalLink, FileCode2, GitFork, Plus, RefreshCw, ShieldCheck, ShieldOff, Terminal, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { api, SignedOutError } from "./api.js";
import { AgentBadge, AgentBadges } from "./agent-badge.jsx";
import { agentSummary, harnessLabel } from "./agents.js";
import { relativeTime } from "./display.jsx";
import { HarnessMark } from "./harness-mark.jsx";
import { agentFrontMatter, agentOverridden, dayLabel, durationShort, factoryUpdateLine, githubLine, groupAgents, pollLabel, roleLabel, scopeLabel, stepKind, stepKindLabel, stepShellPreview, tokenKind, tokenKinds, tokenLine, tokenPrefixLabel, validateTokenName } from "./settings.js";
import { sentence, SettingsHead } from "./setup-page.jsx";

// resourceCache keeps the last loaded response per API key so switching
// Settings tabs renders immediately while revalidating in the background.
const resourceCache = new Map();
let cacheGeneration = 0;

export function clearSettingsCache() {
  cacheGeneration++;
  resourceCache.clear();
}

// useResource loads what a page shows from one or more API routes.
function useResource(paths, onSignedOut) {
  const key = paths.join(" ");
  const [data, setDataState] = useState(() => resourceCache.get(key) ?? null);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const version = useRef(0);

  const setData = useCallback(next => {
    version.current += 1;
    setDataState(prev => {
      const value = typeof next === "function" ? next(prev) : next;
      if (value === null || value === undefined) resourceCache.delete(key);
      else resourceCache.set(key, value);
      return value;
    });
    setRefreshing(false);
  }, [key]);

  useEffect(() => {
    let cancelled = false;
    const generation = cacheGeneration;
    const requestVersion = version.current;
    setRefreshing(true);
    Promise.all(key.split(" ").map(path => api(path)))
      .then(found => {
        if (cancelled || generation !== cacheGeneration || requestVersion !== version.current) return;
        resourceCache.set(key, found);
        setDataState(found);
        setError("");
      })
      .catch(failure => {
        if (cancelled || generation !== cacheGeneration || requestVersion !== version.current) return;
        if (failure instanceof SignedOutError) onSignedOut();
        else setError(sentence(failure.message));
      })
      .finally(() => {
        if (!cancelled && generation === cacheGeneration && requestVersion === version.current) setRefreshing(false);
      });
    return () => { cancelled = true; };
  }, [key, onSignedOut]);

  return { data, error, refreshing, setData };
}

function FactsSkeleton({ rows }) {
  return <dl className="list settings-facts" aria-hidden="true">
    {rows.map(([labelWidth, valueWidth, hintWidth], index) => <div key={index} className="settings-fact">
      <dt><span className="skeleton skeleton-label" style={{ width: labelWidth }} /></dt>
      <dd className="skeleton-stack">
        <span className="skeleton skeleton-title" style={{ width: valueWidth }} />
        {hintWidth && <span className="skeleton skeleton-text" style={{ width: hintWidth }} />}
      </dd>
    </div>)}
  </dl>;
}

function GitHubSkeleton() {
  return <div className="settings-skeleton" role="status" aria-busy="true">
    <span className="sr-only">Asking GitHub</span>
    <FactsSkeleton rows={[
      ["64px", "124px", "72%"],
      ["88px", "184px", "60%"],
      ["82px", "54px", "68%"],
    ]} />
    <section className="settings-section" aria-hidden="true">
      <span className="skeleton skeleton-heading" />
      <span className="skeleton skeleton-text" style={{ width: "68%" }} />
      <ul className="list">
        {[["152px", "48px"], ["132px", "56px"]].map(([repoWidth, branchWidth], index) => <li key={index} className="settings-row">
          <div className="settings-row-main">
            <span className="skeleton skeleton-title" style={{ width: repoWidth }} />
            <span className="skeleton skeleton-label" style={{ width: branchWidth }} />
          </div>
          <span className="skeleton skeleton-pill" />
        </li>)}
      </ul>
    </section>
  </div>;
}

function LinearSkeleton() {
  return <div className="settings-skeleton" role="status" aria-busy="true">
    <span className="sr-only">Loading Linear</span>
    <FactsSkeleton rows={[
      ["76px", "136px", "64%"],
      ["96px", "172px", "54%"],
      ["64px", "96px", ""],
      ["88px", "112px", ""],
    ]} />
  </div>;
}

function TokensSkeleton() {
  return <div className="settings-skeleton" role="status" aria-busy="true">
    <span className="sr-only">Loading tokens</span>
    <ul className="list" aria-hidden="true">
      {[["92px", "64px"], ["116px", "72px"], ["84px", "68px"]].map(([nameWidth, pillWidth], index) => <li key={index} className="settings-row token-row">
        <div className="settings-row-main">
          <span className="skeleton skeleton-title" style={{ width: nameWidth }} />
          <span className="skeleton skeleton-pill" style={{ width: pillWidth }} />
        </div>
        <div className="token-actions">
          <span className="skeleton skeleton-button-sm" />
          <span className="skeleton skeleton-button-sm" style={{ width: "64px" }} />
        </div>
        <div className="token-times">
          <span className="skeleton skeleton-meta" style={{ width: "104px" }} />
          <span className="skeleton skeleton-meta" style={{ width: "124px" }} />
          <span className="skeleton skeleton-meta" style={{ width: "112px" }} />
        </div>
      </li>)}
    </ul>
  </div>;
}

function PresetsSkeleton() {
  return <div className="settings-skeleton" role="status" aria-busy="true">
    <span className="sr-only">Loading presets</span>
    <section className="settings-section" aria-hidden="true">
      <span className="skeleton skeleton-heading" style={{ width: "76px" }} />
      <ul className="preset-cards">
        {[["88px", "58%"], ["108px", "48%"]].map(([nameWidth, descWidth], cardIndex) => <li key={cardIndex} className="panel preset-card">
          <div className="preset-card-head">
            <div className="settings-row-main">
              <span className="skeleton skeleton-title" style={{ width: nameWidth }} />
              <span className="skeleton skeleton-pill" style={{ width: "62px" }} />
            </div>
            <span className="skeleton skeleton-text" style={{ width: descWidth }} />
          </div>
          <div className="pipeline preset-pipeline">
            <div className="skeleton-pipeline-nodes">
              {[0, 1, 2].map(stepIndex => <Fragment key={stepIndex}>
                {stepIndex > 0 && <span className="pipeline-edge" />}
                <div className="skeleton-pipeline-node">
                  <span className="skeleton skeleton-title" style={{ width: stepIndex === 1 ? "78px" : "64px" }} />
                  <span className="skeleton skeleton-meta" style={{ width: "56px" }} />
                </div>
              </Fragment>)}
            </div>
          </div>
        </li>)}
      </ul>
    </section>
    <section className="settings-section" aria-hidden="true">
      <span className="skeleton skeleton-heading" style={{ width: "68px" }} />
      <ul className="list">
        {[["96px", "148px"], ["84px", "132px"], ["112px", "120px"]].map(([nameWidth, summaryWidth], index) => <li key={index} className="skeleton-agent-row">
          <span className="skeleton skeleton-dot" />
          <span className="skeleton skeleton-dot" />
          <span className="skeleton skeleton-title" style={{ width: nameWidth }} />
          <span className="skeleton skeleton-meta" style={{ width: summaryWidth }} />
        </li>)}
      </ul>
    </section>
  </div>;
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
  const { data, error, refreshing } = useResource(["/api/v1/settings/github"], onSignedOut);
  const settings = data?.[0];
  const loading = !settings && !error;
  return <>
    <SettingsHead title="GitHub" sub={settings && githubLine(settings)} loading={loading} refreshing={Boolean(settings && refreshing)} />
    {error && <p className="form-error" role="alert">{error}</p>}
    {loading && <GitHubSkeleton />}
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
  const { data, error, refreshing } = useResource(["/api/v1/settings/linear"], onSignedOut);
  const linear = data?.[0];
  const loading = !linear && !error;
  return <>
    <SettingsHead
      title="Linear"
      sub={linear && (linear.enabled ? "Issues assigned to the Machinist app start jobs." : "Off. Jobs start from the web app, the API and schedules.")}
      loading={loading}
      refreshing={Boolean(linear && refreshing)}
      actionPlaceholder={<span className="skeleton skeleton-pill" aria-hidden="true" />}
    >
      {linear && <OnOff on={linear.enabled} />}
    </SettingsHead>
    {error && <p className="form-error" role="alert">{error}</p>}
    {loading && <LinearSkeleton />}
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
  const { data, error, refreshing, setData } = useResource(["/api/v1/tokens"], onSignedOut);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState("operator");
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
  const selectedKind = tokenKind(kind);

  useEffect(() => () => { window.clearTimeout(copyTimer.current); }, []);

  function openCreate() {
    setCreating(true);
    setConfirming(null);
    setFailure("");
  }

  function closeCreate() {
    setCreating(false);
    setName("");
    setKind("operator");
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
      const body = { name: nameCheck.name, role: selectedKind.role };
      if (selectedKind.scope) body.scope = selectedKind.scope;
      const created = await api("/api/v1/tokens", { method: "POST", body });
      setData([{ tokens: created.tokens }]);
      window.clearTimeout(copyTimer.current);
      setRevealed({ token: created.token, name: created.name, role: created.role, scope: created.scope, action: "created" });
      setCopied(false);
      setCopyError("");
      setCreating(false);
      setName("");
      setKind("operator");
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
      setRevealed({ token: next.token, name: next.name, role: next.role, scope: next.scope, action: "regenerated" });
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

  const loading = !tokens && !error;
  return <>
    <SettingsHead
      title="Tokens"
      sub={tokens && tokenLine(tokens)}
      loading={loading}
      refreshing={Boolean(tokens && refreshing)}
      actionPlaceholder={<span className="skeleton skeleton-button" aria-hidden="true" />}
    >
      {tokens && !creating && <Button size="sm" onClick={openCreate} disabled={busy}><Plus className="size-4" aria-hidden="true" />New token</Button>}
    </SettingsHead>
    <p className="page-note">People, bots, and runners authenticate with tokens. Only a token's hash and short prefix are kept, so each full token is shown once, when it is made.</p>
    {creating && <form className="panel token-form" aria-label="New token" onSubmit={create}>
      <div className="token-form-fields">
        <div>
          <label className="field-label" htmlFor="token-name">Name</label>
          <input id="token-name" className="field-control" value={name} onChange={event => setName(event.target.value)} placeholder="e.g. owain, grokbot, or mac-mini" autoComplete="off" autoFocus />
          {nameCheck.error && <p className="form-error">{nameCheck.error}</p>}
        </div>
        <div>
          <label className="field-label" htmlFor="token-role">Role</label>
          <Select id="token-role" value={kind} onChange={setKind}>
            {tokenKinds.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
          </Select>
          <p className="ticket-meta token-role-hint">{selectedKind.hint}</p>
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
    {error && <p className="form-error" role="alert">{error}</p>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {loading && <TokensSkeleton />}
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
  const [role, about] = roleLabel(token.role, token.scope);
  const scope = scopeLabel(token.scope);
  const revoked = Boolean(token.revoked_at);
  return <li className="settings-row token-row" data-revoked={revoked || undefined}>
    <div className="settings-row-main">
      <strong>{token.name}</strong>
      {token.current && <span className="pill pill-info">You</span>}
      <span className="pill" title={about}>{role}</span>
      {scope && <span className="pill" title={scope[1]}>{scope[0]}</span>}
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
        <span className="ticket-meta">{token.current ? "This signs you out." : token.role === "runner" ? "Its runner stops." : token.scope ? "Its API and MCP access ends." : "Its sign-ins end."}</span>
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

function PresetCard({ preset, isDefault, agentsByName }) {
  const [selectedIndex, setSelectedIndex] = useState(null);
  const selectedStep = selectedIndex !== null ? preset.steps[selectedIndex] : null;
  const timeout = durationShort(preset.timeout_millis);
  const toggleStep = index => setSelectedIndex(current => (current === index ? null : index));

  return <li className="panel preset-card">
    <div className="preset-card-head">
      <div className="settings-row-main">
        <strong>{preset.name}</strong>
        {isDefault && <span className="pill pill-info">Default</span>}
        {preset.extends && <span className="pill preset-extends"><GitFork className="size-3" aria-hidden="true" />extends <code>{preset.extends}</code></span>}
        {(preset.harness || preset.model) && <span className="agent-badge preset-override-badge" data-harness={preset.harness || undefined}>
          {preset.harness && <HarnessMark harness={preset.harness} className="agent-badge-mark" />}
          <span>{preset.harness ? harnessLabel(preset.harness) : "Model override"}</span>
          {preset.model && <span className="agent-badge-model">{preset.model}</span>}
        </span>}
        {timeout && <span className="pill">{timeout}</span>}
      </div>
      {preset.description && <p className="ticket-meta">{preset.description}</p>}
    </div>
    <section className="pipeline preset-pipeline" aria-label={`Pipeline for ${preset.name}`}>
      <ol className="preset-step-list" aria-label={`Steps of ${preset.name}`}>{preset.steps.map((step, index) => {
        const kind = stepKind(step);
        const kindText = stepKindLabel(step);
        const stepTimeout = durationShort(step.timeout_millis);
        const shellPreview = stepShellPreview(step);
        const selected = selectedIndex === index;
        const overridden = (step.agents || []).some(agent => agentOverridden(agent, agentsByName.get(agent.name)));
        return <li key={`${step.name}-${index}`} data-kind={kind} data-selected={selected || undefined}>
          {index > 0 && <span className="pipeline-edge" aria-hidden="true" />}
          <button type="button" className="pipeline-node preset-pipeline-node" aria-expanded={selected} onClick={() => toggleStep(index)} aria-label={`Inspect step ${index + 1}, ${step.name} (${kindText})`}>
            <span className="preset-step-number" aria-hidden="true">{index + 1}</span>
            <span className="pipeline-name step-name">{step.name}</span>
            <span className="pipeline-meta">
              {kind === "shell" && <Terminal className="size-3 inline-icon" aria-hidden="true" />}
              {kindText}
              {stepTimeout ? ` · ${stepTimeout}` : ""}
              {overridden ? " · overridden" : ""}
            </span>
            {step.agents?.length > 0
              ? <AgentBadges agents={step.agents} step={step.name} compact className="pipeline-agents" />
              : shellPreview && <span className="pipeline-shell-cmd" title={shellPreview}>{shellPreview}</span>}
          </button>
        </li>;
      })}</ol>
    </section>
    {selectedStep && <StepInspector step={selectedStep} index={selectedIndex} agentsByName={agentsByName} onClose={() => setSelectedIndex(null)} />}
  </li>;
}

function StepInspector({ step, index, agentsByName, onClose }) {
  const kind = stepKind(step);
  const timeout = durationShort(step.timeout_millis);
  const idleTimeout = durationShort(step.idle_timeout_millis);
  const scripts = step.scripts || [];
  const uniqueAgents = [];
  const seen = new Set();
  for (const agent of step.agents || []) {
    const key = `${agent.name}\u0000${agent.harness}\u0000${agent.model || ""}`;
    if (!seen.has(key)) {
      seen.add(key);
      uniqueAgents.push(agent);
    }
  }
  return <section className="inspector step-inspector" aria-label={`Details for step ${step.name}`}>
    <div className="inspector-head">
      <div className="settings-row-main">
        <span className="preset-step-number" aria-hidden="true">{index + 1}</span>
        <strong className="step-name">{step.name}</strong>
        <span className="pill">{stepKindLabel(step)}</span>
        {timeout && <span className="ticket-meta">Timeout {timeout}</span>}
        {idleTimeout && <span className="ticket-meta">Idle timeout {idleTimeout}</span>}
      </div>
      <Button type="button" variant="ghost" size="sm" onClick={onClose} aria-label="Close step details"><X className="size-3.5" aria-hidden="true" />Close</Button>
    </div>
    {step.run && <div className="inspector-block">
      <h3 className="label">{kind === "agent" ? "Invocation" : "Command"}</h3>
      <pre className="inspector-code">{step.run}</pre>
    </div>}
    {scripts.map(script => <div key={script.path} className="inspector-block">
      <h3 className="inspector-file-label"><FileCode2 className="size-3.5" aria-hidden="true" /><code>factory/{script.path}</code></h3>
      <pre className="inspector-code">{script.content.replace(/\n$/, "")}</pre>
    </div>)}
    {uniqueAgents.map((stepAgent, agentIndex) => {
      const fileAgent = agentsByName.get(stepAgent.name);
      const overridden = agentOverridden(stepAgent, fileAgent);
      const frontMatter = agentFrontMatter(fileAgent);
      return <div key={`${stepAgent.name}-${agentIndex}`} className="inspector-agent">
        <div className="inspector-agent-head">
          <span className="inspector-file-label"><code>factory/agents/{stepAgent.name}.md</code></span>
          <AgentBadge agent={stepAgent} />
          {overridden && <span className="pill pill-info">Overridden</span>}
          {overridden && fileAgent && <span className="ticket-meta">File default: {harnessLabel(fileAgent.harness)}{fileAgent.model ? ` · ${fileAgent.model}` : " default"}</span>}
        </div>
        {frontMatter && <div className="inspector-block">
          <h4 className="label">Front matter</h4>
          <pre className="inspector-code">{`---\n${frontMatter}\n---`}</pre>
        </div>}
        <div className="inspector-block">
          <h4 className="label">Prompt</h4>
          {fileAgent?.prompt
            ? <pre className="inspector-prompt">{fileAgent.prompt}</pre>
            : <p className="ticket-meta">Prompt not available.</p>}
        </div>
      </div>;
    })}
    {!step.run && !scripts.length && !uniqueAgents.length && <p className="ticket-meta">No command or script details available.</p>}
  </section>;
}

function AgentGroupRow({ group, fileAgentsByName }) {
  const [open, setOpen] = useState(false);
  const { primary, variants, presets, uses, harnesses, prompt } = group;
  const multi = variants.length > 1;
  const usageLabel = !presets.length
    ? "No preset"
    : presets.length <= 3
      ? presets.join(", ")
      : `${presets.length} presets`;

  return <li className="agent-row" data-expanded={open || undefined}>
    <button type="button" className="agent-row-toggle" aria-expanded={open} onClick={() => setOpen(value => !value)}>
      <ChevronRight className="size-4 step-chevron" aria-hidden="true" />
      <span className="agent-row-marks">
        {harnesses.map(harness => <HarnessMark key={harness} harness={harness} className="size-4" />)}
      </span>
      <span className="agent-row-name">
        <strong>{primary.name}</strong>
        {multi && <span className="pill">{variants.length} variants · same prompt</span>}
      </span>
      <span className="agent-row-summary">
        {multi
          ? <span className="ticket-meta">{variants.map(v => v.name).join(", ")}</span>
          : primary.model
            ? <span className="ticket-meta">{harnessLabel(primary.harness)} · <code>{primary.model}</code></span>
            : <span className="ticket-meta">{agentSummary(primary)}</span>}
        <span className="ticket-meta agent-row-usage">{usageLabel}</span>
      </span>
    </button>
    {open && <div className="inspector agent-inspector">
      {multi
        ? <div className="inspector-block">
          <h3 className="label">Variants sharing this prompt</h3>
          <ul className="inspector-variants">{variants.map(variant => {
            const frontMatter = agentFrontMatter(variant);
            const variantUses = variant.uses?.length
              ? variant.uses
              : (variant.presets || []).map(preset => ({ preset, step: "", harness: variant.harness, model: variant.model || "" }));
            return <li key={variant.name} className="inspector-variant">
              <div className="inspector-agent-head">
                <HarnessMark harness={variant.harness} className="size-4" />
                <code>factory/agents/{variant.name}.md</code>
                <span className="ticket-meta">{variant.model ? `${harnessLabel(variant.harness)} · ${variant.model}` : agentSummary(variant)}</span>
              </div>
              {frontMatter && <pre className="inspector-code">{`---\n${frontMatter}\n---`}</pre>}
              {variantUses.length > 0 && <ul className="inspector-uses">{variantUses.map((use, idx) => <li key={`${use.preset}-${use.step}-${idx}`}>
                <span className="inspector-use-target"><code>{use.preset}</code>{use.step && <><span>→</span><code>{use.step}</code></>}</span>
                <AgentBadge agent={{ name: variant.name, harness: use.harness, model: use.model }} step={use.step || variant.name} />
                {agentOverridden(use, variant) && <span className="pill pill-info">Overridden</span>}
              </li>)}</ul>}
            </li>;
          })}</ul>
        </div>
        : <>
          <div className="inspector-agent-head">
            <span className="inspector-file-label"><code>factory/agents/{primary.name}.md</code></span>
            <span className="ticket-meta">{harnessLabel(primary.harness)}{primary.model ? ` · ${primary.model}` : " default"}</span>
          </div>
          {agentFrontMatter(primary) && <div className="inspector-block">
            <h3 className="label">Front matter</h3>
            <pre className="inspector-code">{`---\n${agentFrontMatter(primary)}\n---`}</pre>
          </div>}
          {uses.length > 0 && <div className="inspector-block">
            <h3 className="label">Used by</h3>
            <ul className="inspector-uses">{uses.map((use, idx) => {
              const fileAgent = fileAgentsByName.get(use.agent) || primary;
              return <li key={`${use.preset}-${use.step}-${idx}`}>
                <span className="inspector-use-target"><code>{use.preset}</code>{use.step && <><span>→</span><code>{use.step}</code></>}</span>
                <AgentBadge agent={{ name: fileAgent.name, harness: use.harness, model: use.model }} step={use.step || fileAgent.name} />
                {agentOverridden(use, fileAgent) && <span className="pill pill-info">Overridden</span>}
              </li>;
            })}</ul>
          </div>}
        </>}
      <div className="inspector-block">
        <h3 className="label">{multi ? "Shared prompt" : "Prompt"}</h3>
        {prompt
          ? <pre className="inspector-prompt">{prompt}</pre>
          : <p className="ticket-meta">No prompt body.</p>}
      </div>
    </div>}
  </li>;
}

export function PresetsPage({ onSignedOut }) {
  const { data, error, refreshing } = useResource(["/api/v1/presets", "/api/v1/agents"], onSignedOut);
  const [presets, agents] = data || [];
  const [showPresetSpecific, setShowPresetSpecific] = useState(false);
  const [showUnused, setShowUnused] = useState(false);
  const updates = presets?.updates || [];
  const agentsByName = new Map((agents?.agents || []).map(agent => [agent.name, agent]));
  const grouped = groupAgents(agents?.agents || []);
  const presetSpecificCount = grouped.presetGroups.reduce((sum, entry) => sum + entry.groups.length, 0);
  const hasActiveAgents = grouped.primary.length > 0 || presetSpecificCount > 0;
  const loading = !data && !error;

  return <>
    <SettingsHead
      title="Presets and agents"
      sub={data && `${presets.presets.length} ${presets.presets.length === 1 ? "preset" : "presets"} and ${agents.agents.length} ${agents.agents.length === 1 ? "agent" : "agents"}.`}
      loading={loading}
      refreshing={Boolean(data && refreshing)}
    />
    <p className="page-note">These come from <code>factory/presets</code> and <code>factory/agents</code>. Click any step or agent to inspect its script, front matter, or prompt. Change them in git and restart the control plane.</p>
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
    {error && <p className="form-error" role="alert">{error}</p>}
    {loading && <PresetsSkeleton />}
    {data && <>
      <section className="settings-section" aria-labelledby="presets-title">
        <h2 id="presets-title" className="chart-title">Presets</h2>
        <ul className="preset-cards">{presets.presets.map(preset => <PresetCard key={preset.name} preset={preset} isDefault={preset.name === presets.default} agentsByName={agentsByName} />)}</ul>
      </section>
      <section className="settings-section" aria-labelledby="agents-title">
        <h2 id="agents-title" className="chart-title">Agents</h2>
        {agents.agents.length
          ? <>
            {grouped.primary.length > 0 && <div className="agent-section-group">
              {grouped.hasShared && <h3 className="label">Reusable roles</h3>}
              <ul className="list" aria-label={grouped.hasShared ? "Reusable agents" : "Agents"}>
                {grouped.primary.map(group => <AgentGroupRow key={group.key} group={group} fileAgentsByName={agentsByName} />)}
              </ul>
            </div>}
            {grouped.presetGroups.length > 0 && (grouped.hasShared
              ? <div className="token-revoked-section">
                <Button type="button" variant="ghost" size="sm" className="token-revoked-toggle" aria-expanded={showPresetSpecific} onClick={() => setShowPresetSpecific(open => !open)}>
                  <ChevronRight className={cn("size-3.5 token-revoked-chevron", showPresetSpecific && "token-revoked-chevron-open")} aria-hidden="true" />
                  {showPresetSpecific ? `Hide preset-specific (${presetSpecificCount})` : `Show preset-specific (${presetSpecificCount})`}
                </Button>
                {showPresetSpecific && <div className="agent-preset-groups">{grouped.presetGroups.map(({ preset, groups }) => <div key={preset} className="agent-section-group">
                  <h3 className="label">Only in {preset}</h3>
                  <ul className="list" aria-label={`Agents only in ${preset}`}>
                    {groups.map(group => <AgentGroupRow key={group.key} group={group} fileAgentsByName={agentsByName} />)}
                  </ul>
                </div>)}</div>}
              </div>
              : <div className="agent-preset-groups">{grouped.presetGroups.map(({ preset, groups }) => <div key={preset} className="agent-section-group">
                <h3 className="label">{preset}</h3>
                <ul className="list" aria-label={`Agents in ${preset}`}>
                  {groups.map(group => <AgentGroupRow key={group.key} group={group} fileAgentsByName={agentsByName} />)}
                </ul>
              </div>)}</div>)}
            {grouped.unused.length > 0 && (hasActiveAgents
              ? <div className="token-revoked-section">
                <Button type="button" variant="ghost" size="sm" className="token-revoked-toggle" aria-expanded={showUnused} onClick={() => setShowUnused(open => !open)}>
                  <ChevronRight className={cn("size-3.5 token-revoked-chevron", showUnused && "token-revoked-chevron-open")} aria-hidden="true" />
                  {showUnused ? `Hide unused (${grouped.unused.length})` : `Show unused (${grouped.unused.length})`}
                </Button>
                {showUnused && <ul className="list" aria-label="Unused agents">
                  {grouped.unused.map(group => <AgentGroupRow key={group.key} group={group} fileAgentsByName={agentsByName} />)}
                </ul>}
              </div>
              : <ul className="list" aria-label="Unused agents">
                {grouped.unused.map(group => <AgentGroupRow key={group.key} group={group} fileAgentsByName={agentsByName} />)}
              </ul>)}
          </>
          : <p className="empty">No agents. A step runs one with <code>machinist agent &lt;name&gt;</code>.</p>}
      </section>
    </>}
  </>;
}
