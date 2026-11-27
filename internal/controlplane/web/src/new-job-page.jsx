import React, { useEffect, useRef, useState } from "react";
import { ArrowLeft, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { api, SignedOutError } from "./api.js";
import { onlineHarnessSet } from "./automations.js";
import { canStart, harnessOptionLabel, initialPreset, keyFor, modelPlaceholder, runRequest, supportedHarnesses, titleOf } from "./new-job.js";
import { jobHref } from "./routes.js";
import { AgentBadges } from "./agent-badge.jsx";
import { HarnessMark } from "./harness-mark.jsx";
import { presetAgents } from "./agents.js";

// NewJobPage starts a job through the run API, like curl or a tracker would.
export function NewJobPage({ onSignedOut }) {
  const [options, setOptions] = useState(null);
  const [preset, setPreset] = useState("");
  const [repo, setRepo] = useState("");
  const [harness, setHarness] = useState("");
  const [model, setModel] = useState("");
  const [prompt, setPrompt] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const lastKey = useRef(null);
  const mounted = useRef(true);
  useEffect(() => () => { mounted.current = false; }, []);

  useEffect(() => {
    let stopped = false;
    const loadRunners = api("/api/v1/runners").catch(failure => {
      if (failure instanceof SignedOutError) throw failure;
      return { runners: [] };
    });
    Promise.all([api("/api/v1/presets"), api("/api/v1/repositories"), loadRunners]).then(([presets, repositories, runners]) => {
      if (stopped) return;
      setOptions({
        presets: presets.presets,
        repositories: repositories.repositories,
        runners: Array.isArray(runners?.runners) ? runners.runners : [],
      });
      setPreset(initialPreset(presets.presets, presets.default));
      setRepo(repositories.repositories[0] || "");
    }).catch(failure => {
      if (stopped) return;
      if (failure instanceof SignedOutError) onSignedOut();
      else setError(failure.message);
    });
    return () => { stopped = true; };
  }, [onSignedOut]);

  function changeHarness(next) {
    setHarness(next);
    if (next !== harness) setModel("");
  }

  const ready = canStart({ preset, repo, prompt }) && !pending;
  async function submit(event) {
    event.preventDefault();
    if (!ready) return;
    setPending(true);
    setError("");
    const fields = { preset, repo, prompt };
    if (harness.trim()) fields.harness = harness.trim();
    if (model.trim()) fields.model = model.trim();
    lastKey.current = keyFor(lastKey.current, fields);
    try {
      const job = await api("/machinist/run", { method: "POST", body: runRequest({ key: lastKey.current.key, ...fields }) });
      if (mounted.current) window.location.hash = jobHref(job.id);
    } catch (failure) {
      if (!mounted.current) return;
      if (failure instanceof SignedOutError) onSignedOut();
      else setError(failure.message);
      setPending(false);
    }
  }
  function submitOnShortcut(event) {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) submit(event);
  }

  const chosen = options?.presets.find(item => item.name === preset);
  const onlineHarnesses = onlineHarnessSet(options?.runners);
  const title = titleOf(prompt);
  return <div className="page new-job-page">
    <a className="back-link" href="#/jobs"><ArrowLeft className="size-3.5" aria-hidden="true" />Jobs</a>
    <header className="page-head"><h1>New job</h1></header>
    {!options && !error && <p className="page-note" role="status">Loading presets</p>}
    {options && !options.repositories.length && <p className="page-warning" role="alert">No repositories are connected. Add one on the <a href="#/settings/projects">Projects</a> page.</p>}
    {options && <form className="new-job" onSubmit={submit}>
      <div>
        <label className="field-label" htmlFor="new-job-repo">Repository</label>
        <Select id="new-job-repo" value={repo} onChange={setRepo} disabled={!options.repositories.length}>
          {options.repositories.map(name => <option key={name} value={name}>{name}</option>)}
        </Select>
      </div>
      <div>
        <label className="field-label" htmlFor="new-job-preset">Preset</label>
        <Select id="new-job-preset" value={preset} onChange={setPreset} aria-describedby="new-job-preset-about">
          {options.presets.map(item => <option key={item.name} value={item.name}>{item.name}</option>)}
        </Select>
        {chosen && <div id="new-job-preset-about" className="preset-about">
          {chosen.description && <p>{chosen.description}</p>}
          <ol className="preset-steps" aria-label="Steps">{chosen.steps.map(step => <li key={step.name}><span>{step.name}{step.agents.map((agent, index) => <HarnessMark key={index} harness={agent.harness} className="preset-step-mark" />)}</span></li>)}</ol>
          {presetAgents(chosen).length > 0 && <p className="preset-agents"><span className="preset-agents-label">Agents</span><AgentBadges agents={presetAgents(chosen)} /></p>}
        </div>}
      </div>
      <div className="new-job-fields-row">
        <div>
          <label className="field-label" htmlFor="new-job-harness">Harness <span className="field-optional">(optional)</span></label>
          <Select id="new-job-harness" value={harness} onChange={changeHarness} disabled={pending}>
            <option value="">Preset default</option>
            {supportedHarnesses.map(item => <option key={item} value={item}>{harnessOptionLabel(item, onlineHarnesses)}</option>)}
          </Select>
        </div>
        <div>
          <label className="field-label" htmlFor="new-job-model">Model <span className="field-optional">(optional)</span></label>
          <input
            id="new-job-model"
            className="field-control"
            value={model}
            onChange={event => setModel(event.target.value)}
            placeholder={modelPlaceholder(harness)}
            disabled={pending}
          />
        </div>
      </div>
      <div>
        <label className="field-label" htmlFor="new-job-prompt">Prompt</label>
        <textarea id="new-job-prompt" className="field-control new-job-prompt" rows={8} value={prompt} onChange={event => setPrompt(event.target.value)} onKeyDown={submitOnShortcut} placeholder="What should the agent do? Paste a spec or describe the change." />
        <p className="field-hint">{title ? <>Title: <strong>{title}</strong></> : "The first line becomes the job's title."}</p>
      </div>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="new-job-actions">
        <Button type="submit" disabled={!ready}><Play className="size-4" aria-hidden="true" />{pending ? "Starting" : "Start job"}</Button>
        <span className="field-hint new-job-shortcut">Ctrl or ⌘ + Enter</span>
      </div>
    </form>}
    {!options && error && <p className="form-error" role="alert">{error}</p>}
  </div>;
}
