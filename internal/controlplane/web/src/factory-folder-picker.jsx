import React, { useEffect, useRef, useState } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { ArrowUp, Folder, Home, X } from "lucide-react";
import { Button } from "./components/ui/button.jsx";
import { Input } from "./components/ui/input.jsx";
import { factoryRequest } from "./factory-state.js";
export function FolderPicker({
  host,
  hostName,
  initialPath,
  csrfToken,
  onChoose,
  onClose,
}) {
  const [listing, setListing] = useState(null),
    [path, setPath] = useState(initialPath),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const sequence = useRef(0);
  async function load(next) {
    const current = ++sequence.current;
    setBusy(true);
    setError("");
    try {
      const result = await factoryRequest(
        "/folders?host=" +
          encodeURIComponent(host) +
          "&path=" +
          encodeURIComponent(next),
        { headers: { "X-Machinist-CSRF": csrfToken } },
      );
      if (current === sequence.current) {
        setListing(result);
        setPath(result.path);
      }
    } catch (e) {
      if (current === sequence.current) setError(e.message);
    } finally {
      if (current === sequence.current) setBusy(false);
    }
  }
  useEffect(() => {
    load(initialPath);
    return () => {
      sequence.current++;
    };
  }, [host]);
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="folder-picker-overlay" />
        <Dialog.Content className="folder-picker">
          <header>
            <div>
              <Dialog.Title>Choose a folder</Dialog.Title>
              <Dialog.Description>
                Browse folders on {hostName}.
              </Dialog.Description>
            </div>
            <Dialog.Close asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label="Close folder picker"
              >
                <X size={18} />
              </Button>
            </Dialog.Close>
          </header>
          <div className="folder-picker-navigation">
            <Button
              variant="ghost"
              disabled={busy}
              aria-label="Browse home folder"
              onClick={() => load("")}
            >
              <Home size={16} />
              Home
            </Button>
            <Button
              variant="ghost"
              disabled={busy || !listing?.parent}
              onClick={() => load(listing.parent)}
            >
              <ArrowUp size={16} />
              Parent folder
            </Button>
          </div>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              load(path);
            }}
            className="folder-picker-path"
          >
            <Input
              aria-label="Folder path"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="Absolute folder path"
            />
            <Button variant="outline" disabled={busy}>
              Go
            </Button>
          </form>
          {error && (
            <p className="factory-error" role="alert">
              {error}
            </p>
          )}
          <div className="folder-picker-list" aria-label="Folders">
            {busy ? (
              <p role="status">Loading folders…</p>
            ) : listing?.folders?.length ? (
              listing.folders.map((folder) => (
                <button
                  key={folder.path}
                  type="button"
                  onClick={() => load(folder.path)}
                >
                  <Folder size={17} />
                  <span>{folder.name}</span>
                  <small>Open folder</small>
                </button>
              ))
            ) : (
              listing && <p>No subfolders here.</p>
            )}
          </div>
          {listing?.truncated && (
            <p className="factory-muted">
              Only the first 200 folders are shown. Enter a path to open another
              folder.
            </p>
          )}
          <footer>
            <small>{listing?.path}</small>
            <Button
              disabled={busy || !listing || !!error}
              onClick={() => {
                onChoose(listing.path);
                onClose();
              }}
            >
              Use this folder
            </Button>
          </footer>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
