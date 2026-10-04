import { FolderGit2, GitBranch } from "lucide-react";
import React, { useRef, useState } from "react";
import { Input, Select, Label } from "./components/ui/input.jsx";
import { FolderPicker } from "./factory-folder-picker.jsx";
import { cloneDestination, parentFolder } from "./factory-folder-state.js";
import { Button } from "./components/ui/button.jsx";
import { factoryRequest } from "./factory-state.js";

export function ProjectSetup({ status, onCreated, onCancel }) {
  const [source, setSource] = useState("folder");
  const [host, setHost] = useState("local");
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [gitURL, setGitURL] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [picking, setPicking] = useState(false);
  const requestID = useRef(null);
  const hosts = status.hosts?.length
    ? status.hosts
    : [{ id: "local", name: "This computer" }];
  const hostName =
    host === "local"
      ? "this computer"
      : hosts.find((h) => h.id === host)?.name || host;
  function changed(update) {
    requestID.current = null;
    setError("");
    update();
  }
  async function submit(event) {
    event.preventDefault();
    if (busy) return;
    requestID.current ||= crypto.randomUUID();
    setBusy(true);
    setError("");
    try {
      const result = await factoryRequest("/projects", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Machinist-CSRF": status.csrf_token,
        },
        body: JSON.stringify({
          request_id: requestID.current,
          name: name.trim(),
          host,
          source,
          path: path.trim(),
          ...(source === "git" ? { git_url: gitURL.trim() } : {}),
        }),
      });
      await onCreated(result.project);
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="factory-page factory-setup">
      <p>
        Connect a repository. Your foreman will manage design, building, checks,
        and review.
      </p>
      <form onSubmit={submit}>
        <fieldset disabled={busy}>
          <legend>Code source</legend>
          <div className="factory-source-options">
            <label className="factory-source">
              <input
                type="radio"
                name="source"
                checked={source === "folder"}
                onChange={() => changed(() => setSource("folder"))}
              />
              <FolderGit2 size={18} />
              <span>
                <strong>Existing repository</strong>
                <small>
                  Use a Git repository already on the selected host.
                </small>
              </span>
            </label>
            <label className="factory-source">
              <input
                type="radio"
                name="source"
                checked={source === "git"}
                onChange={() => changed(() => setSource("git"))}
              />
              <GitBranch size={18} />
              <span>
                <strong>Clone from Git</strong>
                <small>
                  Clone once, then reuse the checkout for future tasks.
                </small>
              </span>
            </label>
          </div>
          <Label htmlFor="project-name">Project name</Label>
          <Input
            id="project-name"
            required
            value={name}
            onChange={(e) => changed(() => setName(e.target.value))}
            placeholder="My app"
            autoFocus
          />
          <Label htmlFor="project-host">Run workers on</Label>
          <Select
            id="project-host"
            value={host}
            onChange={(e) => changed(() => setHost(e.target.value))}
          >
            {hosts.map((h) => (
              <option key={h.id} value={h.id}>
                {h.id === "local" ? "This computer" : h.name || h.id}
              </option>
            ))}
          </Select>
          {host !== "local" && (
            <p className="factory-muted">
              This host needs Git, an authenticated agent runtime, and your
              build tools. All project work stays on this host.
            </p>
          )}
          {source === "git" && (
            <>
              <Label htmlFor="project-git-url">Git URL</Label>
              <Input
                id="project-git-url"
                required
                value={gitURL}
                onChange={(e) => changed(() => setGitURL(e.target.value))}
                placeholder="https://github.com/you/repository.git"
              />
              <small>
                Use HTTPS or SSH. Private repositories use the selected host’s
                Git credentials.
              </small>
            </>
          )}
          <Label htmlFor="project-path">
            {source === "folder" ? "Repository folder" : "Clone into folder"}
          </Label>
          <div className="factory-folder-field">
            <Input
              id="project-path"
              required
              value={path}
              onChange={(e) => changed(() => setPath(e.target.value))}
              placeholder={
                host === "local"
                  ? "/Users/you/Code/my-app"
                  : "/home/you/projects/my-app"
              }
              aria-describedby="project-path-help"
            />
            <Button
              type="button"
              variant="outline"
              onClick={() => setPicking(true)}
            >
              Browse folders
            </Button>
          </div>
          <small id="project-path-help">
            An absolute path on {hostName}.{" "}
            {source === "folder"
              ? "The folder must contain a Git repository with at least one commit."
              : "Use a new folder. Existing folders are never replaced."}
          </small>
        </fieldset>
        {error && (
          <p role="alert" className="factory-error">
            {error}
          </p>
        )}
        <div className="factory-form-actions">
          <Button disabled={busy}>
            {busy
              ? source === "git"
                ? "Cloning repository…"
                : "Checking repository…"
              : "Add project"}
          </Button>
          <Button
            type="button"
            variant="ghost"
            disabled={busy}
            onClick={onCancel}
          >
            Cancel
          </Button>
        </div>
        {busy && (
          <p role="status" className="factory-muted">
            Connecting to {hostName}. You can start work when setup finishes.
          </p>
        )}
      </form>
      {picking && (
        <FolderPicker
          host={host}
          hostName={hostName}
          initialPath={source === "git" && path ? parentFolder(path) : path}
          csrfToken={status.csrf_token}
          onClose={() => setPicking(false)}
          onChoose={(folder) =>
            changed(() =>
              setPath(
                source === "git"
                  ? cloneDestination(folder, path, gitURL, name)
                  : folder,
              ),
            )
          }
        />
      )}
    </div>
  );
}
