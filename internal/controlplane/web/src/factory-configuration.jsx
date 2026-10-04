import React, { useEffect, useState } from "react";
import { Check, ClipboardList, Settings2 } from "lucide-react";
import { Button } from "./components/ui/button.jsx";
import { Input, Label, Select } from "./components/ui/input.jsx";
import {
  TabsRoot,
  TabsList,
  TabsTrigger,
  TabsContent,
} from "./components/ui/tabs.jsx";
import { factoryRequest } from "./factory-state.js";
import {
  commandLines,
  configurationErrors,
  copyConfiguration,
} from "./factory-configuration-state.js";
import "./factory-configuration.css";
export function PipelineSettings({ csrfToken, onSaved }) {
  const [saved, setSaved] = useState(null),
    [draft, setDraft] = useState(null),
    [error, setError] = useState(""),
    [conflict, setConflict] = useState(false),
    [loading, setLoading] = useState(true),
    [saving, setSaving] = useState(false),
    [notice, setNotice] = useState(""),
    [tab, setTab] = useState("pipeline"),
    [pipelineID, setPipelineID] = useState(""),
    [stepID, setStepID] = useState(""),
    [agentID, setAgentID] = useState("");
  const headers = { "X-Machinist-CSRF": csrfToken };
  function apply(configuration) {
    setSaved(copyConfiguration(configuration));
    setDraft(copyConfiguration(configuration));
    setPipelineID((current) =>
      configuration.pipelines[current]
        ? current
        : configuration.default_pipeline,
    );
    setAgentID((current) =>
      configuration.agents[current] ? current : configuration.foreman,
    );
    setStepID("");
    setConflict(false);
    setError("");
  }
  async function load() {
    setLoading(true);
    setError("");
    try {
      apply(await factoryRequest("/configuration", { headers }));
    } catch (e) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    let active = true;
    factoryRequest("/configuration", { headers })
      .then((configuration) => {
        if (active) apply(configuration);
      })
      .catch((e) => {
        if (active) setError(e.message);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);
  const dirty = draft && JSON.stringify(draft) !== JSON.stringify(saved);
  function update(edit) {
    setDraft((current) => {
      const next = copyConfiguration(current);
      edit(next);
      return next;
    });
    setNotice("");
  }
  async function save(event) {
    event.preventDefault();
    if (saving) return;
    const invalid = configurationErrors(draft);
    if (invalid.length) {
      setError(invalid.join(" "));
      return;
    }
    setSaving(true);
    setError("");
    setConflict(false);
    try {
      const result = await factoryRequest("/configuration", {
        method: "PUT",
        headers: { ...headers, "Content-Type": "application/json" },
        body: JSON.stringify(draft),
      });
      setSaved(copyConfiguration(result));
      setDraft(copyConfiguration(result));
      setNotice("Saved. New tasks and foreman messages will use these settings.");
      try {
        await onSaved?.();
      } catch {
        setError(
          "Settings were saved, but the factory status could not refresh. Reload the page to refresh its labels.",
        );
      }
    } catch (e) {
      setError(e.message);
      setConflict(e.status === 409);
    } finally {
      setSaving(false);
    }
  }
  function openAgent(id) {
    setAgentID(id);
    setTab("agents");
  }
  if (loading && !draft)
    return (
      <section className="pipeline-settings">
        <h2>Pipeline settings</h2>
        <p role="status">Loading configuration…</p>
      </section>
    );
  if (!draft)
    return (
      <section className="pipeline-settings">
        <h2>Pipeline settings</h2>
        <p role="alert" className="configuration-error">
          {error}
        </p>
        <Button variant="outline" onClick={load}>
          Try again
        </Button>
      </section>
    );
  const pipeline = draft.pipelines[pipelineID],
    step = pipeline?.steps.find((step) => step.id === stepID),
    agent = draft.agents[agentID];
  return (
    <section className="pipeline-settings">
      <header className="configuration-heading">
        <div>
          <h2>Pipeline settings</h2>
          <p>
            Shared across projects. Changes apply to future tasks; current runs
            keep their saved settings.
          </p>
        </div>
        {dirty && (
          <span className="configuration-unsaved">Unsaved changes</span>
        )}
      </header>
      <form onSubmit={save}>
        <fieldset disabled={saving || loading} className="configuration-fields">
          <TabsRoot value={tab} onValueChange={setTab}>
            <TabsList
              className="configuration-tabs"
              aria-label="Configuration section"
            >
              <TabsTrigger value="pipeline">
                <ClipboardList size={15} />
                Pipeline
              </TabsTrigger>
              <TabsTrigger value="agents">
                <Settings2 size={15} />
                Agents
              </TabsTrigger>
            </TabsList>
            <TabsContent value="pipeline">
              <div className="configuration-pipeline-heading">
                <div>
                  <Label htmlFor="configuration-pipeline">Pipeline</Label>
                  <Select
                    id="configuration-pipeline"
                    value={pipelineID}
                    onChange={(e) => {
                      setPipelineID(e.target.value);
                      setStepID("");
                    }}
                  >
                    {Object.entries(draft.pipelines).map(([id, pipeline]) => (
                      <option key={id} value={id}>
                        {pipeline.name}
                      </option>
                    ))}
                  </Select>
                </div>
                {draft.default_pipeline === pipelineID ? (
                  <span className="configuration-default">
                    <Check size={13} />
                    Default for new tasks
                  </span>
                ) : (
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() =>
                      update((next) => {
                        next.default_pipeline = pipelineID;
                      })
                    }
                  >
                    Use as default
                  </Button>
                )}
              </div>
              <ol className="configuration-pipeline">
                {pipeline?.steps.map((item, index) => (
                  <li key={item.id}>
                    <button
                      type="button"
                      aria-pressed={item.id === stepID}
                      onClick={() => setStepID(item.id)}
                    >
                      <small>
                        {index + 1} · {item.stage}
                      </small>
                      <strong>{item.name}</strong>
                      <span>
                        {item.type === "agent"
                          ? draft.agents[item.agent]?.name || item.agent
                          : item.type === "script"
                            ? "Script"
                            : "Human approval"}
                      </span>
                    </button>
                  </li>
                ))}
              </ol>
              {step ? (
                <section className="configuration-step-editor">
                  <header>
                    <div>
                      <h3>{step.name}</h3>
                      <p>
                        {step.stage} ·{" "}
                        {step.type === "agent"
                          ? "Agent"
                          : step.type === "script"
                            ? "Script"
                            : "Human approval"}
                      </p>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      onClick={() => setStepID("")}
                    >
                      Back to pipeline
                    </Button>
                  </header>
                  <Label htmlFor="configuration-step-name">Step name</Label>
                  <Input
                    id="configuration-step-name"
                    required
                    value={step.name}
                    onChange={(e) =>
                      update((next) => {
                        next.pipelines[pipelineID].steps.find(
                          (s) => s.id === stepID,
                        ).name = e.target.value;
                      })
                    }
                  />
                  {step.type === "agent" ? (
                    <>
                      <Label htmlFor="configuration-step-agent">
                        Agent profile
                      </Label>
                      <div className="configuration-agent-choice">
                        <Select
                          id="configuration-step-agent"
                          value={step.agent}
                          onChange={(e) =>
                            update((next) => {
                              next.pipelines[pipelineID].steps.find(
                                (s) => s.id === stepID,
                              ).agent = e.target.value;
                            })
                          }
                        >
                          {Object.entries(draft.agents).map(([id, agent]) => (
                            <option key={id} value={id}>
                              {agent.name}
                            </option>
                          ))}
                        </Select>
                        <Button
                          type="button"
                          variant="outline"
                          onClick={() => openAgent(step.agent)}
                        >
                          Edit profile
                        </Button>
                      </div>
                      <p className="configuration-help">
                        Profiles contain reusable instructions and runtime
                        settings. Editing a profile updates every future step
                        that uses it.
                      </p>
                    </>
                  ) : step.type === "script" ? (
                    <>
                      <Label htmlFor="configuration-command">
                        Command arguments
                      </Label>
                      <textarea
                        id="configuration-command"
                        required
                        className="configuration-command"
                        rows={6}
                        value={(step.command || []).join("\n")}
                        onChange={(e) =>
                          update((next) => {
                            next.pipelines[pipelineID].steps.find(
                              (s) => s.id === stepID,
                            ).command = commandLines(e.target.value);
                          })
                        }
                      />
                      <p className="configuration-help">
                        One argument per line, starting with the executable.
                        Spaces stay inside each argument. This command runs
                        directly without shell expansion.
                      </p>
                      <Label htmlFor="configuration-script-timeout">
                        Timeout
                      </Label>
                      <Input
                        id="configuration-script-timeout"
                        value={step.timeout || ""}
                        placeholder="Default: 10m"
                        onChange={(e) =>
                          update((next) => {
                            next.pipelines[pipelineID].steps.find(
                              (s) => s.id === stepID,
                            ).timeout = e.target.value;
                          })
                        }
                      />
                    </>
                  ) : (
                    <p className="configuration-help">
                      This approval is required. Its stage and position stay
                      fixed.
                    </p>
                  )}
                </section>
              ) : (
                <p className="configuration-help">
                  Select a step to edit its settings. The stage order and
                  required approvals stay fixed.
                </p>
              )}
            </TabsContent>
            <TabsContent value="agents">
              <div className="configuration-foreman">
                <Label htmlFor="configuration-foreman">Foreman profile</Label>
                <Select
                  id="configuration-foreman"
                  value={draft.foreman}
                  onChange={(e) =>
                    update((next) => {
                      next.foreman = e.target.value;
                    })
                  }
                >
                  {Object.entries(draft.agents).map(([id, agent]) => (
                    <option key={id} value={id}>
                      {agent.name}
                    </option>
                  ))}
                </Select>
                <p className="configuration-help">
                  The foreman is the agent you talk to. This change applies when
                  the next foreman turn starts.
                </p>
              </div>
              <div className="configuration-agent-layout">
                <nav aria-label="Agent profiles">
                  {Object.entries(draft.agents).map(([id, item]) => (
                    <button
                      key={id}
                      type="button"
                      aria-current={agentID === id ? "true" : undefined}
                      onClick={() => setAgentID(id)}
                    >
                      <strong>{item.name || id}</strong>
                      <small>
                        {id === draft.foreman
                          ? "Foreman"
                          : item.description || "Agent profile"}
                      </small>
                    </button>
                  ))}
                </nav>
                {agent && (
                  <section className="configuration-profile">
                    <h3>{agent.name || agentID}</h3>
                    <Label htmlFor="configuration-agent-name">Name</Label>
                    <Input
                      id="configuration-agent-name"
                      required
                      value={agent.name}
                      onChange={(e) =>
                        update((next) => {
                          next.agents[agentID].name = e.target.value;
                        })
                      }
                    />
                    <Label htmlFor="configuration-agent-purpose">Purpose</Label>
                    <textarea
                      id="configuration-agent-purpose"
                      rows={2}
                      value={agent.description || ""}
                      onChange={(e) =>
                        update((next) => {
                          next.agents[agentID].description = e.target.value;
                        })
                      }
                    />
                    <Label htmlFor="configuration-agent-prompt">
                      Instructions
                    </Label>
                    <textarea
                      id="configuration-agent-prompt"
                      required
                      rows={9}
                      value={agent.prompt}
                      onChange={(e) =>
                        update((next) => {
                          next.agents[agentID].prompt = e.target.value;
                        })
                      }
                    />
                    <div className="configuration-profile-fields">
                      <div>
                        <Label htmlFor="configuration-agent-runtime">
                          Runtime
                        </Label>
                        <Select
                          id="configuration-agent-runtime"
                          value="claude"
                          disabled
                        >
                          <option value="claude">Claude Code</option>
                        </Select>
                      </div>
                      <div>
                        <Label htmlFor="configuration-agent-model">Model</Label>
                        <Input
                          id="configuration-agent-model"
                          value={agent.model || ""}
                          placeholder="Runtime default"
                          onChange={(e) =>
                            update((next) => {
                              next.agents[agentID].model = e.target.value;
                            })
                          }
                        />
                      </div>
                      <div>
                        <Label htmlFor="configuration-agent-timeout">
                          Timeout
                        </Label>
                        <Input
                          id="configuration-agent-timeout"
                          required
                          value={agent.timeout}
                          placeholder="30m"
                          onChange={(e) =>
                            update((next) => {
                              next.agents[agentID].timeout = e.target.value;
                            })
                          }
                        />
                      </div>
                    </div>
                    <p className="configuration-help">
                      Leave the model empty to use the runtime default. Timeouts
                      accept durations such as 90s, 30m, or 1h.
                    </p>
                  </section>
                )}
              </div>
            </TabsContent>
          </TabsRoot>
        </fieldset>
        {error && (
          <div className="configuration-error" role="alert">
            <p>{error}</p>
            {conflict && (
              <>
                <p>
                  Your edits are still here. Reload the saved configuration
                  before applying changes to its latest version.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    if (
                      !dirty ||
                      window.confirm(
                        "Reloading will replace your unsaved settings. Continue?",
                      )
                    )
                      load();
                  }}
                >
                  Reload saved settings
                </Button>
              </>
            )}
          </div>
        )}
        {notice && (
          <p className="configuration-notice" role="status">
            {notice}
          </p>
        )}
        <footer className="configuration-save">
          <p>
            {dirty
              ? "Your edits have not been saved."
              : "Current runs keep their original definitions."}
          </p>
          <div>
            <Button
              type="button"
              variant="ghost"
              disabled={!dirty || saving}
              onClick={() => {
                setDraft(copyConfiguration(saved));
                setConflict(false);
                setError("");
                setNotice("");
              }}
            >
              Discard changes
            </Button>
            <Button disabled={!dirty || saving}>
              {saving ? "Saving…" : "Save settings"}
            </Button>
          </div>
        </footer>
      </form>
    </section>
  );
}
