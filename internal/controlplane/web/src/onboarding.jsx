import React, { useEffect, useState } from "react";
import { Check, GitBranch, ArrowRight, RefreshCw } from "lucide-react";
import { Button } from "./components/ui/button";

export async function onboardingRequest(path, csrfToken, body) {
  const response = await fetch(`/api/v1/onboarding${path}`, body === undefined ? { headers: { Accept: "application/json" } } : { method: "POST", headers: { "Content-Type": "application/json", "X-Machinist-CSRF": csrfToken }, body: JSON.stringify(body) });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(result.error || `Unable to continue (${response.status}). Please try again.`);
  return result;
}

export function Onboarding({ csrfToken, onReady }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [device, setDevice] = useState(null);
  const [repository, setRepository] = useState("");
  const [path, setPath] = useState("");
  const [agent, setAgent] = useState("codex");
  const [host, setHost] = useState("local");
  const [clientID, setClientID] = useState("");
  const [installURL, setInstallURL] = useState("");
  const [stage, setStage] = useState(null);
  async function load() {
    const next = await onboardingRequest("", csrfToken);
    setData(next);
    setRepository(current => current || next.selection?.repository || "");
    setAgent(next.selection?.agent || "codex");
    setHost(next.selection?.host || "local");
    return next;
  }
  useEffect(() => { let active = true; onboardingRequest("", csrfToken).then(next => { if (!active) return; setData(next); setRepository(next.selection?.repository || ""); setAgent(next.selection?.agent || "codex"); setHost(next.selection?.host || "local"); }).catch(e => active && setError(e.message)); return () => { active = false; }; }, [csrfToken]);
  async function run(action) { setBusy(true); setError(""); try { await action(); } catch (e) { setError(e.message); } finally { setBusy(false); } }
  useEffect(() => {
    if (!device) return;
    let stopped = false;
    let timer;
    const expires = Date.now() + (device.expires_in || 900) * 1000;
    let interval = Math.max(5, device.interval || 5);
    async function poll() {
      if (Date.now() >= expires) { setDevice(null); setError("The sign-in code expired. Connect GitHub again to get a new code."); return; }
      try {
        const result = await onboardingRequest("/github/poll", csrfToken, {});
        if (stopped) return;
        if (result.status === "connected") { setDevice(null); await load(); return; }
        if (!["pending", "authorization_pending", "slow_down"].includes(result.status)) throw new Error(result.message || "GitHub sign-in was not completed. Try connecting again.");
        interval = result.interval || (result.status === "slow_down" ? interval + 5 : interval);
        timer = setTimeout(poll, interval * 1000);
      } catch (e) { if (!stopped) { setDevice(null); setError(e.message); } }
    }
    timer = setTimeout(poll, interval * 1000);
    return () => { stopped = true; clearTimeout(timer); };
  }, [device, csrfToken]);
  const automatic = !data?.github?.connected ? 0 : !data?.selection?.repository ? 1 : !data?.selection?.agent ? 2 : 3;
  const step = stage ?? automatic;
  const steps = ["GitHub", "Repository", "Agent", "Ready"];
  return <div className="onboarding-page">
    <header><h1>Set up Machinist</h1><p>Connect your repository. Bring your own agent. Run on infrastructure you control.</p></header>
    <ol className="onboarding-steps" aria-label="Setup progress">{steps.map((name, index) => <li key={name} aria-current={step === index ? "step" : undefined}><span>{index < automatic ? <Check size={14} /> : index + 1}</span>{name}</li>)}</ol>
    {error && <div className="onboarding-error" role="alert">{error}{!data && <Button variant="outline" onClick={() => run(load)}>Try again</Button>}</div>}
    {!data ? <p role="status">Loading connection settings…</p> : <section className="onboarding-content" aria-label={steps[step]}>
      {step === 0 && <><GitBranch size={28} /><h2>Connect GitHub</h2><p>Choose the repositories Machinist can access. Specs, pull requests, and useful updates stay on GitHub. Agent logs and internal state stay on the execution host.</p>
        {data.github?.connected ? <><p className="onboarding-confirmed"><Check size={16} />Connected as {data.github.account}</p><Button onClick={() => setStage(1)}>Choose a repository<ArrowRight size={16} /></Button><Button variant="ghost" disabled={busy} onClick={() => run(async () => { await onboardingRequest("/github/disconnect", csrfToken, {}); setDevice(null); setStage(null); await load(); })}>Disconnect</Button></> : <>
        {device ? <div className="device-authorization"><span>Enter this code on GitHub</span><code>{device.user_code}</code><a className="onboarding-link" href={device.verification_uri} target="_blank" rel="noreferrer">Open GitHub to sign in<ArrowRight size={16} /></a><p role="status">Waiting for GitHub authorization…</p><Button variant="ghost" onClick={() => setDevice(null)}>Cancel sign-in</Button></div> : <Button disabled={busy || !data.github?.configured} onClick={() => run(async () => setDevice(await onboardingRequest("/github/start", csrfToken, {})))}><GitBranch size={16} />{busy ? "Connecting…" : "Connect GitHub"}</Button>}
        {data.github?.install_url && <p><a href={data.github.install_url} target="_blank" rel="noreferrer">Install the GitHub App or manage repository access</a></p>}
        {!data.github?.configured && <p>The Machinist GitHub App needs to be registered before sign-in is available.</p>}
        <section><h3>Use your own GitHub App</h3><form onSubmit={e => { e.preventDefault(); run(async () => { await onboardingRequest("/github/configure", csrfToken, { client_id: clientID, install_url: installURL }); await load(); }); }}><label>Public client ID<input className="field-control" required value={clientID} onChange={e => setClientID(e.target.value)} /></label><label>Installation URL<input className="field-control" type="url" required value={installURL} onChange={e => setInstallURL(e.target.value)} /></label><p>Enable Device Flow in the app settings. No client secret or private key is needed here.</p><Button disabled={busy}>Save app connection</Button></form></section></>}
      </>}
      {step === 1 && <><h2>Choose a repository</h2><p>Use an existing checkout or let Machinist clone the repository on this computer.</p><form onSubmit={e => { e.preventDefault(); run(async () => { await onboardingRequest("/repository", csrfToken, { repository, path }); await load(); setStage(2); }); }}><label>Repository<select className="field-control" required value={repository} onChange={e => setRepository(e.target.value)}><option value="">Select a repository</option>{(data.repositories || []).map(repo => <option key={repo.full_name} value={repo.full_name}>{repo.full_name}{repo.private ? " · Private" : ""}</option>)}</select></label>{!data.repositories?.length && <p>No repositories available. Check the GitHub App’s repository access, then refresh. {data.github?.install_url && <a href={data.github.install_url} target="_blank" rel="noreferrer">Install the GitHub App or manage repository access</a>}</p>}<Button type="button" variant="ghost" disabled={busy} onClick={() => run(async () => { const repositories = await onboardingRequest("/repositories", csrfToken); setData(current => ({ ...current, repositories: Array.isArray(repositories) ? repositories : repositories?.repositories || [] })); })}><RefreshCw size={14} />Refresh repositories</Button><section><h3>Use an existing checkout</h3><label>Checkout path<input className="field-control" value={path} onChange={e => setPath(e.target.value)} placeholder="Leave blank to clone automatically" /></label></section><div className="onboarding-actions"><Button type="button" variant="ghost" onClick={() => setStage(0)}>Back</Button><Button disabled={busy || !repository}>{busy ? "Preparing repository…" : "Continue"}<ArrowRight size={16} /></Button></div></form></>}
      {step === 2 && <><h2>Choose your agent</h2><p>Run agents on this computer, a VM, or your configured Kubernetes cluster.</p><form onSubmit={e => { e.preventDefault(); run(async () => { await onboardingRequest("/agent", csrfToken, { agent, host }); await load(); setStage(3); }); }}><fieldset><legend>Execution host</legend><label className="onboarding-choice"><input type="radio" name="host" value="local" checked={host === "local"} onChange={() => setHost("local")} /><span>This computer</span></label><label className="onboarding-choice"><input type="radio" name="host" value="remote" checked={host === "remote"} onChange={() => setHost("remote")} /><span>Remote VM</span></label>{data.kubernetes && <label className="onboarding-choice"><input type="radio" name="host" value="kubernetes" checked={host === "kubernetes"} onChange={() => {setHost("kubernetes");setAgent(data.kubernetes_agent || "codex");}} /><span>Kubernetes<small>Private gVisor jobs · {data.kubernetes_agent === "claude" ? "Claude" : "Codex"} subscription</small></span></label>}</fieldset><fieldset><legend>Agent</legend>{(data.agents?.length ? data.agents : [{ id: "codex", name: "Codex", available: false }, { id: "claude", name: "Claude Code", available: false }]).map(item => <label className="onboarding-choice" key={item.id}><input type="radio" name="agent" value={item.id} disabled={host === "kubernetes" && item.id !== (data.kubernetes_agent || "codex")} checked={agent === item.id} onChange={() => setAgent(item.id)} /><span>{item.name}<small>{(host === "kubernetes" ? "Runs in the configured cluster" : item.detail) || (item.available ? "Detected on this computer" : "Not detected on this computer")}</small></span></label>)}</fieldset>{host === "remote" && <p>Connect a worker from your VM before verification. Machinist does not provision or upload your code to a hosted agent service.</p>}<div className="onboarding-actions"><Button variant="ghost" type="button" onClick={() => setStage(1)}>Back</Button><Button disabled={busy}>{busy ? "Saving…" : "Continue"}<ArrowRight size={16} /></Button></div></form></>}
      {step === 3 && <><h2>{data.ready ? "You’re ready to build" : "Verify your connection"}</h2><p>Run a small PING to verify the agent, repository, and permissions. This check creates no GitHub issue.</p><dl className="onboarding-summary"><dt>GitHub</dt><dd>{data.github?.account}</dd><dt>Repository</dt><dd>{data.selection?.repository}</dd><dt>Agent</dt><dd>{data.selection?.agent === "codex" ? "Codex" : data.selection?.agent === "claude" ? "Claude Code" : data.selection?.agent} · {({remote:"Remote VM",kubernetes:"Kubernetes",local:"This computer"}[data.selection?.host] || "This computer")}</dd></dl>{host === "remote" && !data.worker?.connected && <div><p>Run the SSH tunnel on your VM to reach this computer, which must allow SSH connections. Then run the downloaded setup script on the VM. Install Git, GitHub CLI, and your chosen agent there and sign in. Verification waits for a matching worker to connect.</p><a href={data.worker?.config_url || "/api/v1/onboarding/worker-config?host=remote"} download>Download worker setup</a><p>The setup script contains a worker connection credential. Keep it private.</p>{data.worker?.tunnel && <><p>Run this on the VM to reach this computer:</p><pre className="onboarding-command">{data.worker.tunnel}</pre></>}{data.worker?.command && <pre className="onboarding-command">{data.worker.command}</pre>}<Button variant="ghost" onClick={() => run(load)}>Check connection</Button></div>}{data.check && <p role="status" className={data.check.state === "failed" ? "text-danger" : "onboarding-confirmed"}>{data.check.message}</p>}<div className="onboarding-actions"><Button variant="ghost" onClick={() => setStage(2)}>Back</Button><Button variant="outline" disabled={busy} onClick={() => run(async () => { const check = await onboardingRequest("/check", csrfToken, {}); setData(current => ({ ...current, check, ready: check.state === "passed" })); })}>{busy ? "Checking agent…" : "Run connection check"}</Button><Button disabled={busy || !(data.ready || data.check?.state === "passed")} onClick={() => run(async () => { await onboardingRequest("/finish", csrfToken, {}); await onReady?.(); window.location.hash = "#/runs"; })}>Start using Machinist<ArrowRight size={16} /></Button></div></>}
    </section>}
  </div>;
}
