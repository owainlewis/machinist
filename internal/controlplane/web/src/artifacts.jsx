import { FileText, Download, X } from "lucide-react";
import React, { useEffect, useRef, useState } from "react";

// One metadata request per task, shared by Result, Files, and History.
export function useTaskArtifacts(job, csrfToken) {
  const [state, setState] = useState({ jobID: null, byRun: {}, error: "" });
  useEffect(() => {
    if (!job?.task) return;
    const controller = new AbortController();
    fetch(`/api/v1/jobs/${encodeURIComponent(job.id)}/artifacts`, { headers: { "X-Machinist-CSRF": csrfToken }, signal: controller.signal })
      .then(async r => { if (!r.ok) throw new Error("Could not load outputs"); return r.json(); })
      .then(files => {
        const byRun = Object.create(null);
        for (const file of files) (byRun[file.run_id] ||= []).push(file);
        if (!controller.signal.aborted) setState({ jobID: job.id, byRun, error: "" });
      })
      .catch(e => { if (!controller.signal.aborted) setState({ jobID: job.id, byRun: {}, error: e.message }); });
    return () => controller.abort();
  }, [job?.id, job?.updated_at, Boolean(job?.task), csrfToken]);
  return state.jobID === job?.id ? state : { byRun: {}, error: "" };
}

export function Artifacts({ artifacts, runID, csrfToken }) {
  const previewRequest = useRef(null);
  const [preview, setPreview] = useState(null);
  const [loading, setLoading] = useState("");
  const [error, setError] = useState("");
  const [downloading, setDownloading] = useState("");
  const files = artifacts.byRun[runID] || [];
  useEffect(() => {
    setPreview(null); setLoading(""); setError("");
    return () => previewRequest.current?.abort();
  }, [runID]);
  function canPreview(file) { return file.size <= 1024*1024 && (file.content_type?.startsWith("text/") || /\.(md|txt|json|csv|log|ya?ml|toml|py|js|ts|go|sh|html|xml|css)$/i.test(file.path)); }
  async function view(file) {
    previewRequest.current?.abort();
    const controller = new AbortController();
    previewRequest.current = controller;
    setPreview(null); setLoading(file.id); setError("");
    try {
      const response = await fetch(`/api/v1/artifacts/${encodeURIComponent(file.id)}/content`, {headers:{"X-Machinist-CSRF":csrfToken}, signal: controller.signal});
      if (!response.ok) throw new Error(response.status === 410 ? "This file has expired." : "Could not open file");
      const body = await response.text();
      if (body.includes("\0")) throw new Error("This file is binary. Download it to view it.");
      if (!controller.signal.aborted) setPreview({file,body});
    } catch(e) { if (!controller.signal.aborted) setError(e.message); }
    finally { if (!controller.signal.aborted) setLoading(""); }
  }
  async function download(file) {
    setError("");
    setDownloading(file.id);
    try {
      const r = await fetch(`/api/v1/artifacts/${encodeURIComponent(file.id)}/content`, { headers: { "X-Machinist-CSRF": csrfToken } });
      if (!r.ok) throw new Error(r.status === 410 ? "This output has expired." : "Download failed");
      const url = URL.createObjectURL(await r.blob());
      const link = document.createElement("a"); link.href = url; link.download = file.path.split("/").at(-1); link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) { setError(e.message); }
    finally { setDownloading(""); }
  }
  if (!files.length && !error && !artifacts.error) return null;
  return <section aria-label="Files" className="space-y-2">
    {(error || artifacts.error) && <p role="alert" className="text-sm text-danger">{error || artifacts.error}</p>}
    <ul className="divide-y divide-border rounded-lg border border-border">{files.map(file => <li key={file.id} className="flex min-w-0 items-center gap-4 px-4 py-3 text-sm">
      <FileText className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      {file.expired_at ? <span className="min-w-0 flex-1 break-all text-muted-foreground">{file.path}</span> : <button type="button" className="min-w-0 flex-1 break-all text-left font-medium text-primary underline underline-offset-4 hover:decoration-2 focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-60" aria-label={`${canPreview(file) ? "View" : "Download"} ${file.path}`} disabled={Boolean(loading || downloading)} onClick={() => canPreview(file) ? view(file) : download(file)}>{loading === file.id ? "Opening…" : file.path}</button>}
      <span className="shrink-0 text-xs text-muted-foreground">{file.expired_at ? "Expired" : `${(file.size / 1024).toFixed(1)} KB`}</span>
      {!file.expired_at && <button type="button" className="rounded-md p-2 text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring" aria-label={`Download ${file.path}`} disabled={Boolean(downloading)} onClick={()=>download(file)}><Download className="size-4" /></button>}
    </li>)}</ul>
    {preview && <section aria-label={`Preview ${preview.file.path}`} className="overflow-hidden rounded-lg border border-border">
      <div className="flex items-center justify-between gap-3 border-b border-border bg-muted/40 px-4 py-2"><h3 className="min-w-0 break-all text-sm font-medium">{preview.file.path} <span className="text-muted-foreground">· Raw</span></h3><button type="button" className="rounded-md p-2 hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring" aria-label="Close file preview" onClick={()=>setPreview(null)}><X className="size-4" /></button></div>
      <pre tabIndex={0} className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words p-4 font-mono text-xs leading-6">{preview.body}</pre>
    </section>}
  </section>;
}
