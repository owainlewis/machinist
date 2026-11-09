import React, { useState } from "react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";

export function SignIn({ onSignedIn }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
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
  return <main className="sign-in">
    <form onSubmit={submit}>
      <span className="brand-wordmark">Machinist</span>
      <label className="field-label" htmlFor="token">Token</label>
      <input id="token" className="field-control" type="password" autoComplete="current-password" autoFocus value={token} onChange={event => setToken(event.target.value)} />
      {error && <p className="form-error" role="alert">{error}</p>}
      <Button type="submit" disabled={pending || !token.trim()}>{pending ? "Signing in" : "Sign in"}</Button>
    </form>
  </main>;
}
