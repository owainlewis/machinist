import { useState } from "react";
import { Check, ChevronRight, Copy, Plus } from "lucide-react";
import { TemplateHelp, useDefinitions } from "@/catalog";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/dialog";
import { ErrorBanner, TopBar } from "@/components/ui/page-heading";
import { Select } from "@/components/ui/select";
import { Spinner, StatusIcon } from "@/components/ui/status-icon";
import { humanize } from "@/runs-board";

const themes = [{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }];

export function SettingsPage({ status, loaded, error, dark, setDark }) {
  const definitions = useDefinitions();
  const [agent, setAgent] = useState(null);
  const [adding, setAdding] = useState(false);
  const [variables, setVariables] = useState(false);
  const data = definitions.value;
  const workflows = Object.entries(data.workflows || {});
  const repositories = status.repositories || [];
  return <>
    <TopBar title="Settings" />
    {error && <ErrorBanner>{error}</ErrorBanner>}
    <div className="pane-scroll">
      <div className="mx-auto max-w-3xl space-y-8 px-4 py-6 sm:px-6">
        <Section title="Repositories" description="Where agents can work. Each worker lists its own checkouts." action={<Button variant="outline" onClick={() => setAdding(true)}><Plus className="size-3.5" />Add repository</Button>}>
          {!loaded ? <Row><Spinner />Loading</Row>
            : repositories.length ? repositories.map((name) => {
              const online = status.workers.filter((worker) => worker.connected && worker.repositories?.includes(name));
              return <Row key={name}>
                <StatusIcon state={online.length ? "online" : "offline"} title={online.length ? "Available" : "No online worker"} />
                <span className="flex-1 font-medium">{name}</span>
                <span className={online.length ? "text-xs text-faint" : "text-xs tone-warning"}>{online.length ? `On ${[...new Set(online.map((worker) => worker.name))].join(", ")}` : "No online worker has it"}</span>
              </Row>;
            })
            : <Row muted>No repositories yet. Add one to start giving agents work.</Row>}
        </Section>

        <Section title="Agents" description="The prompts and runtimes tasks can use. Defined in config.toml." action={<Button variant="ghost" onClick={() => setVariables(true)}>Prompt variables</Button>}>
          {definitions.loading ? <Row><Spinner />Loading the latest configuration.</Row>
            : definitions.error ? <Row><span role="alert" className="text-danger">{definitions.error}</span></Row>
            : data.commands.length ? data.commands.map((command) => <button key={command.name} type="button" className="flex w-full items-center gap-3 border-b border-border px-4 py-2.5 text-left last:border-b-0 hover:bg-muted" onClick={() => setAgent(command)}>
              <span className="flex-1 font-medium">{command.name}</span>
              <span className="font-mono text-xs text-faint">{command.executor} · {command.timeout}</span>
              <ChevronRight className="size-3.5 text-faint" />
            </button>)
            : <Row muted>Configuration added on the control plane will appear here.</Row>}
        </Section>

        {workflows.length > 0 && <Section title="Workflows" description="Agents chained into steps that run in order.">
          {workflows.map(([name, steps]) => <Row key={name}><span className="font-medium">{humanize(name)}</span><span className="min-w-0 flex-1 truncate text-right text-xs text-faint">{steps.map((step) => `${humanize(step.name)}${step.approval ? " (approval first)" : ""}`).join(" → ")}</span></Row>)}
        </Section>}

        <Section title="Appearance">
          <Row><span className="flex-1">Theme</span><Select label="Theme" value={dark ? "dark" : "light"} onValueChange={(value) => setDark(value === "dark")} items={themes} /></Row>
        </Section>
      </div>
    </div>

    <AddRepositoryModal open={adding} onOpenChange={setAdding} existing={repositories} />
    <Modal open={Boolean(agent)} onOpenChange={(open) => !open && setAgent(null)} title={agent?.name} description={agent && `${agent.executor} · times out after ${agent.timeout}`} className="w-[min(44rem,calc(100vw-2rem))]">
      {agent && <pre tabIndex={0} aria-label={`${agent.name} prompt template`} className="log-block max-h-[60vh] overflow-auto">{agent.prompt || "Uses the task instructions directly."}</pre>}
    </Modal>
    <Modal open={variables} onOpenChange={setVariables} title="Prompt variables" description="Use these in an agent's prompt file.">
      <TemplateHelp />
    </Modal>
  </>;
}

function Section({ title, description, action, children }) {
  return <section aria-label={title}>
    <div className="mb-2 flex items-end gap-3">
      <div className="min-w-0 flex-1"><h2 className="font-semibold">{title}</h2>{description && <p className="mt-0.5 text-xs text-faint">{description}</p>}</div>
      {action}
    </div>
    <div className="overflow-hidden rounded-lg border border-border">{children}</div>
  </section>;
}

function Row({ children, muted = false }) {
  return <div className={`flex min-h-10 items-center gap-3 border-b border-border px-4 py-2 last:border-b-0 ${muted ? "text-faint" : ""}`}>{children}</div>;
}

// Repositories live in each worker's worker.toml, so adding one produces the exact block to paste.
function AddRepositoryModal({ open, onOpenChange, existing }) {
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [copied, setCopied] = useState(false);
  const validName = /^[a-z0-9][a-z0-9_-]*$/.test(name);
  const duplicate = existing.includes(name);
  const validPath = path.startsWith("/") || path.startsWith("~/");
  const snippet = `[repositories.${name || "my-project"}]\npath = "${path || "/absolute/path/to/my-project"}"`;
  const ready = validName && !duplicate && validPath;
  async function copy() {
    try { await navigator.clipboard.writeText(snippet); setCopied(true); setTimeout(() => setCopied(false), 1500); } catch { setCopied(false); }
  }
  return <Modal open={open} onOpenChange={(next) => { onOpenChange(next); if (!next) { setName(""); setPath(""); } }} title="Add repository" description="Point a worker at a Git checkout on its machine."
    footer={<><Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button><Button disabled={!ready} onClick={copy}>{copied ? <><Check className="size-3.5" />Copied</> : <><Copy className="size-3.5" />Copy config</>}</Button></>}>
    <div className="space-y-4">
      <label className="block"><span className="field-label">Name</span><input className="field-control" value={name} onChange={(event) => setName(event.target.value.trim())} placeholder="todo" autoFocus />
        <span className={`mt-1 block text-xs ${name && (!validName || duplicate) ? "text-danger" : "text-faint"}`}>{duplicate ? "A repository with this name already exists." : "Lowercase letters, numbers, - and _. Used when starting tasks."}</span></label>
      <label className="block"><span className="field-label">Path on the worker</span><input className="field-control font-mono text-xs" value={path} onChange={(event) => setPath(event.target.value.trim())} placeholder="/Users/you/Code/todo" />
        <span className={`mt-1 block text-xs ${path && !validPath ? "text-danger" : "text-faint"}`}>An absolute path to a Git checkout, or one starting with ~/.</span></label>
      <div>
        <span className="field-label">Add to worker.toml</span>
        <pre className="log-block">{snippet}</pre>
        <p className="mt-2 text-xs text-faint">Paste this into <code className="font-mono">~/.machinist/worker.toml</code> on that machine, then restart its worker. The repository appears here once the worker reconnects.</p>
      </div>
    </div>
  </Modal>;
}
