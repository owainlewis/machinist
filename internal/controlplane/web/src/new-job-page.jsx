import React, { useEffect, useMemo, useState } from "react";
import { ArrowLeft, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, SignedOutError } from "./api.js";
import { canStart, initialPreset, newJobKey, offersPresetChoice, runRequest, titleOf } from "./new-job.js";
import { jobHref } from "./routes.js";

// NewJobPage starts a job through the run API, like curl or a tracker would.
export function NewJobPage({ onSignedOut }) {
  const [options, setOptions] = useState(null);
  const [preset, setPreset] = useState("");
  const [repo, setRepo] = useState("");
  const [prompt, setPrompt] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const key = useMemo(() => newJobKey(), []);

  useEffect(() => {
    let stopped = false;
    Promise.all([api("/api/v1/presets"), api("/api/v1/repositories")]).then(([presets, repositories]) => {
      if (stopped) return;
      setOptions({ presets: presets.presets, repositories: repositories.repositories });
      setPreset(initialPreset(presets.presets, presets.default));
      setRepo(repositories.repositories[0] || "");
    }).catch(failure => {
      if (stopped) return;
      if (failure instanceof SignedOutError) onSignedOut();
      else setError(failure.message);
    });
    return () => { stopped = true; };
  }, [onSignedOut]);

  const ready = canStart({ preset, repo, prompt }) && !pending;
  async function submit(event) {
    event.preventDefault();
    if (!ready) return;
    setPending(true);
    setError("");
    try {
      const job = await api("/machinist/run", { method: "POST", body: runRequest({ key, preset, repo, prompt }) });
      window.location.hash = jobHref(job.id);
    } catch (failure) {
      if (failure instanceof SignedOutError) onSignedOut();
      else setError(failure.message);
      setPending(false);
    }
  }
  function submitOnShortcut(event) {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) submit(event);
  }

  const chosen = options?.presets.find(item => item.name === preset);
  const title = titleOf(prompt);
  return <div className="page new-job-page">
    <a className="back-link" href="#/jobs"><ArrowLeft className="size-3.5" aria-hidden="true" />Jobs</a>
    <header className="page-head"><h1>New job</h1></header>
    {!options && !error && <p className="page-note" role="status">Loading presets</p>}
    {options && !options.repositories.length && <p className="page-warning" role="alert">No repositories are connected. Add them to <code>github.repositories</code> in <code>config.yaml</code>, then restart the control plane.</p>}
    {options && <form className="new-job" onSubmit={submit}>
      <div>
        <label className="field-label" htmlFor="new-job-repo">Repository</label>
        <select id="new-job-repo" className="field-control" value={repo} onChange={event => setRepo(event.target.value)} disabled={!options.repositories.length}>
          {options.repositories.map(name => <option key={name} value={name}>{name}</option>)}
        </select>
      </div>
      <div>
        <label className="field-label" htmlFor="new-job-prompt">Prompt</label>
        <textarea id="new-job-prompt" className="field-control new-job-prompt" rows={14} value={prompt} onChange={event => setPrompt(event.target.value)} onKeyDown={submitOnShortcut} placeholder={"What should the agent do? Paste a spec or describe the change.\n\nThe first line becomes the job's title."} />
        <p className="field-hint">{title ? <>Title: <strong>{title}</strong></> : "The first line becomes the job's title."}</p>
      </div>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="new-job-actions">
        <Button type="submit" disabled={!ready}><Play className="size-4" aria-hidden="true" />{pending ? "Starting" : "Start job"}</Button>
        <span className="field-hint new-job-shortcut">Ctrl or ⌘ + Enter</span>
        {offersPresetChoice(options.presets) && <div className="new-job-preset">
          <label htmlFor="new-job-preset">Preset</label>
          <select id="new-job-preset" className="field-control" value={preset} onChange={event => setPreset(event.target.value)} title={chosen?.steps?.join(", ")}>
            {options.presets.map(item => <option key={item.name} value={item.name}>{item.name}</option>)}
          </select>
        </div>}
      </div>
    </form>}
    {!options && error && <p className="form-error" role="alert">{error}</p>}
  </div>;
}
