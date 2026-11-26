import React, { useEffect, useState } from "react";
import { Brand } from "@/components/ui/brand";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";

const oauthErrorMessages = {
  disabled: "Your account has been disabled. Contact an administrator to regain access.",
  not_invited: "Your GitHub account is not invited to this Machinist instance. Ask an admin for an invite link.",
  oauth_denied: "GitHub sign-in was cancelled.",
  oauth_failed: "GitHub sign-in could not be completed. Please try again.",
};

function parseHashError(hash) {
  const queryIndex = hash.indexOf("?");
  if (queryIndex < 0) return "";
  const params = new URLSearchParams(hash.slice(queryIndex + 1));
  const code = params.get("error") || "";
  return oauthErrorMessages[code] || "";
}

function parseInviteSecret(hash) {
  if (!hash.startsWith("#/invite#")) return "";
  return hash.slice("#/invite#".length).trim();
}

function buildOAuthStartURL(startUrl, hash) {
  const base = startUrl || "/auth/github/start";
  if (!hash || hash.startsWith("#/sign-in") || hash.startsWith("#/invite")) return base;
  const sep = base.includes("?") ? "&" : "?";
  return `${base}${sep}return_to=${encodeURIComponent("/" + hash)}`;
}

export function SignIn({ authStatus = { methods: [], token_signin_enabled: true }, onSignedIn }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState(() => parseHashError(window.location.hash));
  const [invite, setInvite] = useState(null);
  const [preparingInvite, setPreparingInvite] = useState(false);
  const [pending, setPending] = useState(false);

  const githubEnabled = Array.isArray(authStatus.methods) && authStatus.methods.includes("github");
  const tokenEnabled = Boolean(authStatus.token_signin_enabled);

  useEffect(() => {
    const secret = parseInviteSecret(window.location.hash);
    if (!secret) return;
    // Strip the secret from browser history immediately so it cannot linger.
    window.history.replaceState(null, "", "#/sign-in");
    let cancelled = false;
    setPreparingInvite(true);
    setError("");
    api("/api/v1/invites/prepare", { method: "POST", body: { token: secret } })
      .then(prepared => {
        if (!cancelled) setInvite(prepared);
      })
      .catch(failure => {
        if (!cancelled) setError(failure.message || "This invite link is invalid or has expired.");
      })
      .finally(() => {
        if (!cancelled) setPreparingInvite(false);
      });
    return () => { cancelled = true; };
  }, []);

  async function submit(event) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await api("/api/v1/session", { method: "POST", body: { token: token.trim() } });
      onSignedIn();
    } catch (failure) {
      setError(failure instanceof SignedOutError ? "That token is not valid. Check it, or create one with machinist token create <name>." : failure.message);
    } finally {
      setPending(false);
    }
  }

  const startHref = buildOAuthStartURL(invite?.start_url, window.location.hash);

  return <main className="sign-in">
    <div className="sign-in-card">
      <Brand />
      {preparingInvite && <p className="sign-in-invite">Checking invite link…</p>}
      {invite && <p className="sign-in-invite" role="status">
        You have been invited as <strong>{invite.role}</strong>. Continue with GitHub to accept.
      </p>}
      {githubEnabled && <a className="sign-in-oauth-btn" href={startHref}>Continue with GitHub</a>}
      {githubEnabled && tokenEnabled && <div className="sign-in-divider"><span>or sign in with a token</span></div>}
      {tokenEnabled && <form onSubmit={submit}>
        <label className="field-label" htmlFor="token">Token</label>
        <input id="token" className="field-control" type="password" autoComplete="current-password" autoFocus={!githubEnabled} value={token} onChange={event => setToken(event.target.value)} />
        <Button type="submit" disabled={pending || !token.trim()}>{pending ? "Signing in" : "Sign in"}</Button>
      </form>}
      {error && <p className="form-error" role="alert">{error}</p>}
    </div>
  </main>;
}
