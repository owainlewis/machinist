import React, { useCallback, useEffect, useState } from "react";
import { Check, Copy, RefreshCw, Shield, Trash2, UserPlus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import { relativeTime } from "./display.jsx";
import { dayLabel } from "./settings.js";

function roleBadgeClass(role) {
  if (role === "admin") return "pill pill-info";
  if (role === "member") return "pill pill-success";
  return "pill";
}

function displayRole(role) {
  if (role === "admin") return "Admin";
  if (role === "member") return "Member";
  if (role === "viewer") return "Viewer";
  return role || "";
}

export function AccountPage({ me, onSignedOut }) {
  const [sessions, setSessions] = useState(null);
  const [error, setError] = useState("");
  const [busyID, setBusyID] = useState("");

  const loadSessions = useCallback(async () => {
    try {
      const data = await api("/api/v1/me/sessions");
      setSessions(data.sessions || []);
      setError("");
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    }
  }, [onSignedOut]);

  useEffect(() => { loadSessions(); }, [loadSessions]);

  async function revoke(id, isCurrent) {
    setBusyID(id);
    setError("");
    try {
      await api(`/api/v1/me/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
      if (isCurrent) {
        onSignedOut?.();
        return;
      }
      await loadSessions();
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    } finally {
      setBusyID("");
    }
  }

  const user = me?.user;
  const label = user ? (user.name || user.github_login) : (me?.token_name || "Signed-in session");

  return <div className="settings-section">
    <header className="page-head">
      <div>
        <h1>My Account</h1>
        <p className="page-sub">Your profile, role, and active browser sessions.</p>
      </div>
    </header>

    <section className="panel" aria-labelledby="account-profile-heading">
      <h2 id="account-profile-heading" className="label">Profile</h2>
      <div className="account-profile">
        {user?.avatar_url && <img className="account-avatar" src={user.avatar_url} alt="" width="40" height="40" />}
        <div className="account-meta">
          <div className="account-name-row">
            <strong>{label}</strong>
            <span className={roleBadgeClass(me?.role)}>{displayRole(me?.role)}</span>
          </div>
          {user && <p className="ticket-meta">@{user.github_login}{user.email ? ` · ${user.email}` : ""}</p>}
          {!user && <p className="ticket-meta">Signed in with workspace operator token <code>{me?.token_name}</code></p>}
        </div>
      </div>
    </section>

    <section className="panel" aria-labelledby="account-sessions-heading">
      <h2 id="account-sessions-heading" className="label">Active sessions</h2>
      {error && <p className="form-error" role="alert">{error}</p>}
      {!sessions && !error && <p className="page-note" role="status">Loading sessions</p>}
      {sessions && sessions.length === 0 && <p className="page-note">No active browser sessions.</p>}
      {sessions && sessions.length > 0 && <ul className="list session-list">
        {sessions.map(sess => <li key={sess.id} className="session-item">
          <div className="session-info">
            <div className="session-title">
              <strong>{sess.user_agent || "Browser session"}</strong>
              {sess.current && <span className="pill pill-success">Current</span>}
            </div>
            <p className="ticket-meta">
              {sess.ip_address ? `${sess.ip_address} · ` : ""}
              Last active <time dateTime={sess.last_seen_at}>{relativeTime(sess.last_seen_at)}</time>
              {" · "}Created {dayLabel(sess.created_at)}
            </p>
          </div>
          <Button variant="outline" size="sm" onClick={() => revoke(sess.id, sess.current)} disabled={busyID === sess.id}>
            <Trash2 className="size-3.5" aria-hidden="true" />
            {busyID === sess.id ? "Revoking" : "Revoke"}
          </Button>
        </li>)}
      </ul>}
    </section>
  </div>;
}

export function TeamPage({ onSignedOut }) {
  const [members, setMembers] = useState(null);
  const [invites, setInvites] = useState(null);
  const [error, setError] = useState("");
  const [busyID, setBusyID] = useState("");
  const [targetType, setTargetType] = useState("github");
  const [targetValue, setTargetValue] = useState("");
  const [inviteRole, setInviteRole] = useState("member");
  const [creating, setCreating] = useState(false);
  const [inviteBanner, setInviteBanner] = useState(null);
  const [copied, setCopied] = useState(false);

  const load = useCallback(async () => {
    try {
      const [mData, iData] = await Promise.all([api("/api/v1/team/members"), api("/api/v1/team/invites")]);
      setMembers(mData.members || []);
      setInvites(iData.invites || []);
      setError("");
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    }
  }, [onSignedOut]);

  useEffect(() => { load(); }, [load]);

  const activeAdminCount = (members || []).filter(m => m.role === "admin" && !m.disabled_at).length;

  async function updateMember(id, patch) {
    setBusyID(id);
    setError("");
    try {
      await api(`/api/v1/team/members/${encodeURIComponent(id)}`, { method: "PATCH", body: patch });
      await load();
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    } finally {
      setBusyID("");
    }
  }

  async function createInvite(event) {
    event.preventDefault();
    const val = targetValue.trim();
    if (!val) return;
    setCreating(true);
    setError("");
    setCopied(false);
    try {
      const body = targetType === "github"
        ? { role: inviteRole, github_login: val }
        : { role: inviteRole, email: val };
      const created = await api("/api/v1/team/invites", { method: "POST", body });
      setInviteBanner({ url: created.invite_url, invite: created.invite });
      setTargetValue("");
      await load();
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    } finally {
      setCreating(false);
    }
  }

  async function regenerateInvite(id) {
    setBusyID(id);
    setError("");
    setCopied(false);
    try {
      const regen = await api(`/api/v1/team/invites/${encodeURIComponent(id)}/regenerate`, { method: "POST" });
      setInviteBanner({ url: regen.invite_url, invite: regen.invite });
      await load();
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    } finally {
      setBusyID("");
    }
  }

  async function revokeInvite(id) {
    setBusyID(id);
    setError("");
    try {
      await api(`/api/v1/team/invites/${encodeURIComponent(id)}`, { method: "DELETE" });
      await load();
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut?.();
      else setError(failure.message);
    } finally {
      setBusyID("");
    }
  }

  async function copyInviteURL() {
    if (!inviteBanner?.url) return;
    try {
      await navigator.clipboard?.writeText(inviteBanner.url);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return <div className="settings-section">
    <header className="page-head">
      <div>
        <h1>Team</h1>
        <p className="page-sub">Invite teammates, manage roles, and revoke access without redeploying.</p>
      </div>
    </header>

    {error && <p className="form-error" role="alert">{error}</p>}

    <section className="panel" aria-labelledby="team-invite-heading">
      <h2 id="team-invite-heading" className="label"><UserPlus className="size-3.5" aria-hidden="true" /> Invite member</h2>
      <form className="team-invite-form" onSubmit={createInvite}>
        <div className="team-invite-fields">
          <label className="sr-only" htmlFor="invite-target-type">Target type</label>
          <select id="invite-target-type" className="field-control" value={targetType} onChange={e => setTargetType(e.target.value)}>
            <option value="github">GitHub username</option>
            <option value="email">Email</option>
          </select>
          <label className="sr-only" htmlFor="invite-target-value">{targetType === "github" ? "GitHub username" : "Email address"}</label>
          <input
            id="invite-target-value"
            className="field-control"
            type={targetType === "email" ? "email" : "text"}
            placeholder={targetType === "github" ? "octocat" : "alice@company.com"}
            value={targetValue}
            onChange={e => setTargetValue(e.target.value)}
          />
          <label className="sr-only" htmlFor="invite-role">Role</label>
          <select id="invite-role" className="field-control" value={inviteRole} onChange={e => setInviteRole(e.target.value)}>
            <option value="member">Member</option>
            <option value="viewer">Viewer</option>
            <option value="admin">Admin</option>
          </select>
          <Button type="submit" disabled={creating || !targetValue.trim()}>{creating ? "Creating" : "Create invite"}</Button>
        </div>
      </form>
      {inviteBanner && <div className="team-invite-banner" role="status">
        <p><strong>One-time invite link</strong> — share this directly with the teammate. It will not be shown again.</p>
        <div className="team-invite-url-row">
          <code className="team-invite-url">{inviteBanner.url}</code>
          <Button type="button" variant="outline" size="sm" onClick={copyInviteURL}>
            {copied ? <Check className="size-3.5" aria-hidden="true" /> : <Copy className="size-3.5" aria-hidden="true" />}
            {copied ? "Copied" : "Copy link"}
          </Button>
        </div>
      </div>}
    </section>

    <section className="panel" aria-labelledby="team-members-heading">
      <h2 id="team-members-heading" className="label"><Shield className="size-3.5" aria-hidden="true" /> Members</h2>
      {!members && !error && <p className="page-note" role="status">Loading members</p>}
      {members && members.length === 0 && <p className="page-note">No GitHub team members yet. Create an invite above or configure <code>auth.bootstrap_admin</code>.</p>}
      {members && members.length > 0 && <table className="data-table">
        <thead>
          <tr><th>Member</th><th>Role</th><th>Status</th><th>Last seen</th><th /></tr>
        </thead>
        <tbody>
          {members.map(member => {
            const isDisabled = Boolean(member.disabled_at);
            const isSoleAdmin = member.role === "admin" && !isDisabled && activeAdminCount <= 1;
            const busy = busyID === member.id;
            return <tr key={member.id} data-member={member.github_login || member.id}>
              <td>
                <div className="team-member-cell">
                  {member.avatar_url && <img className="team-avatar" src={member.avatar_url} alt="" width="24" height="24" />}
                  <div>
                    <strong>{member.name || member.github_login}</strong>
                    <div className="ticket-meta">@{member.github_login}{member.email ? ` · ${member.email}` : ""}</div>
                  </div>
                </div>
              </td>
              <td>
                <select
                  aria-label={`Role for ${member.github_login || member.id}`}
                  className="field-control role-select"
                  value={member.role}
                  disabled={busy || isSoleAdmin}
                  title={isSoleAdmin ? "At least one active admin is required" : undefined}
                  onChange={e => updateMember(member.id, { role: e.target.value })}
                >
                  <option value="admin">Admin</option>
                  <option value="member">Member</option>
                  <option value="viewer">Viewer</option>
                </select>
              </td>
              <td><span className={isDisabled ? "pill pill-warning" : "pill pill-success"}>{isDisabled ? "Disabled" : "Active"}</span></td>
              <td className="ticket-meta">{member.last_seen_at ? relativeTime(member.last_seen_at) : "Never"}</td>
              <td className="team-actions">
                {isDisabled
                  ? <Button variant="outline" size="sm" disabled={busy} onClick={() => updateMember(member.id, { disabled: false })}>Re-enable</Button>
                  : <Button variant="outline" size="sm" className="text-danger" disabled={busy || isSoleAdmin} title={isSoleAdmin ? "At least one active admin is required" : undefined} onClick={() => updateMember(member.id, { disabled: true })}>Disable</Button>}
              </td>
            </tr>;
          })}
        </tbody>
      </table>}
    </section>

    <section className="panel" aria-labelledby="team-invites-heading">
      <h2 id="team-invites-heading" className="label">Pending invites</h2>
      {!invites && !error && <p className="page-note" role="status">Loading invites</p>}
      {invites && invites.length === 0 && <p className="page-note">No pending invites.</p>}
      {invites && invites.length > 0 && <table className="data-table">
        <thead>
          <tr><th>Target</th><th>Role</th><th>Expires</th><th /></tr>
        </thead>
        <tbody>
          {invites.map(inv => {
            const target = inv.github_login ? `github:${inv.github_login}` : inv.email;
            const busy = busyID === inv.id;
            return <tr key={inv.id} data-invite={inv.id}>
              <td><code>{target}</code></td>
              <td><span className={roleBadgeClass(inv.role)}>{displayRole(inv.role)}</span></td>
              <td className="ticket-meta">{dayLabel(inv.expires_at)}</td>
              <td className="team-actions">
                <Button variant="ghost" size="sm" disabled={busy} onClick={() => regenerateInvite(inv.id)}>
                  <RefreshCw className="size-3.5" aria-hidden="true" />
                  Regenerate link
                </Button>
                <Button variant="ghost" size="sm" className="text-danger" disabled={busy} onClick={() => revokeInvite(inv.id)}>
                  <Trash2 className="size-3.5" aria-hidden="true" />
                  Revoke
                </Button>
              </td>
            </tr>;
          })}
        </tbody>
      </table>}
    </section>
  </div>;
}
