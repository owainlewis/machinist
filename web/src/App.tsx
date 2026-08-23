import { Bot, Boxes, Columns3, Gauge, GitBranch, GitMerge, Menu, Plus, Repeat2, X } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "./api";
import { RepositoriesView, RepositoryDetail } from "./Repositories";
import { OverviewView } from "./Overview";
import { TasksView } from "./Tasks";
import { PipelinesView } from "./Pipelines";
import { RunDetailView, RunsView, type RunViewMode } from "./Runs";
import { WorkersView, WorkerDetail } from "./Workers";
import { useVisibleInterval } from "./polling";

type Route =
  | { page: "overview" }
  | { page: "tasks"; id?: string; create?: boolean }
  | { page: "pipelines" }
  | { page: "work"; mode: RunViewMode }
  | { page: "run-detail"; id: string; mode: RunViewMode }
  | { page: "workers" }
  | { page: "worker"; id: string }
  | { page: "repositories" }
  | { page: "repository"; id: string };

function readRoute(): Route {
  const parts = window.location.pathname.split("/").filter(Boolean);
  const search = new URLSearchParams(window.location.search);
  const mode = runMode(search.get("view"));
  if (parts[0] === "tasks") return { page: "tasks", id: parts[1], create: search.get("new") === "true" };
  if (parts[0] === "pipelines") return { page: "pipelines" };
  if ((parts[0] === "work" || parts[0] === "runs") && parts[1]) return { page: "run-detail", id: parts[1], mode };
  if (parts[0] === "work" || parts[0] === "runs") return { page: "work", mode };
  if (parts[0] === "overview") return { page: "overview" };
  if (parts[0] === "workers" && parts[1]) return { page: "worker", id: parts[1] };
  if (parts[0] === "workers") return { page: "workers" };
  if (parts[0] === "repositories" && parts[1]) return { page: "repository", id: parts[1] };
  if (parts[0] === "repositories") return { page: "repositories" };
  return { page: "work", mode: "kanban" };
}

function runMode(value: string | null): RunViewMode {
  return value === "list" || value === "table" ? "table" : "kanban";
}

function routePath(route: Route): string {
  switch (route.page) {
    case "tasks": return `/tasks${route.id ? `/${route.id}` : ""}${route.create ? "?new=true" : ""}`;
    case "pipelines": return "/pipelines";
    case "work": return `/work${route.mode === "kanban" ? "" : `?view=${route.mode}`}`;
    case "run-detail": return `/work/${route.id}${route.mode === "kanban" ? "" : `?view=${route.mode}`}`;
    case "workers": return "/workers";
    case "worker": return `/workers/${route.id}`;
    case "repositories": return "/repositories";
    case "repository": return `/repositories/${route.id}`;
    default: return "/overview";
  }
}

export function App() {
  const [route, setRoute] = useState<Route>(readRoute);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const workerInterval = useVisibleInterval(10_000);
  const workers = useQuery({ queryKey: ["workers"], queryFn: api.workers, refetchInterval: workerInterval });
  useEffect(() => {
    const onPopState = () => setRoute(readRoute());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  const navigate = (next: Route) => {
    window.history.pushState({}, "", routePath(next));
    setRoute(next);
    setMobileNavOpen(false);
    window.scrollTo({ top: 0, behavior: "instant" });
  };
  const activeRunMode = route.page === "work" || route.page === "run-detail" ? route.mode : "kanban";
  return <div className="app-shell">
    <aside className={`sidebar ${mobileNavOpen ? "sidebar-open" : ""}`}>
      <div className="brand"><div className="brand-mark" aria-hidden="true"><Boxes size={18} strokeWidth={2.2} /></div><div><span className="brand-name">Factory</span><span className="brand-subtitle">control plane</span></div></div>
      <nav aria-label="Primary navigation">
        <div className="nav-section" role="group" aria-labelledby="work-nav-label">
          <div className="nav-section-label" id="work-nav-label">Work</div>
          <div className="nav-items">
          <Nav active={route.page === "work" || route.page === "run-detail"} icon={<Columns3 size={17} />} label="Work" onClick={() => navigate({ page: "work", mode: activeRunMode })} />
          <Nav active={route.page === "tasks"} icon={<Repeat2 size={17} />} label="Tasks" onClick={() => navigate({ page: "tasks" })} />
          <Nav active={route.page === "pipelines"} icon={<GitMerge size={17} />} label="Pipelines" onClick={() => navigate({ page: "pipelines" })} />
          <Nav active={route.page === "overview"} icon={<Gauge size={17} />} label="Overview" onClick={() => navigate({ page: "overview" })} />
          </div>
        </div>
        <div className="nav-section" role="group" aria-labelledby="infrastructure-nav-label">
          <div className="nav-section-label" id="infrastructure-nav-label">Infrastructure</div>
          <div className="nav-items">
            <Nav active={route.page === "workers" || route.page === "worker"} icon={<Bot size={17} />} label="Workers" onClick={() => navigate({ page: "workers" })} />
            <Nav active={route.page === "repositories" || route.page === "repository"} icon={<GitBranch size={17} />} label="Repositories" onClick={() => navigate({ page: "repositories" })} />
          </div>
        </div>
      </nav>
      <div className="sidebar-foot"><span className="local-dot" aria-hidden="true" /> Local control plane</div>
    </aside>
    <div className="main-shell">
      <header className="topbar"><button className="icon-button mobile-menu" aria-label="Toggle navigation" aria-expanded={mobileNavOpen} onClick={() => setMobileNavOpen((open) => !open)}>{mobileNavOpen ? <X size={19} /> : <Menu size={19} />}</button><div className="topbar-title">{pageTitle(route)}</div><button className="button button-primary" onClick={() => navigate({ page: "tasks", create: true })}><Plus size={15} /> New Task</button></header>
      <main className="app-main">
        {route.page === "overview" && <OverviewView onRun={(id) => navigate({ page: "run-detail", id, mode: "kanban" })} onTask={(id) => navigate({ page: "tasks", id })} />}
        {route.page === "tasks" && <TasksView key={`${route.id ?? "list"}:${route.create ?? false}`} initialID={route.id} createOpen={route.create} onRun={(id) => navigate({ page: "run-detail", id, mode: "kanban" })} />}
        {route.page === "pipelines" && <PipelinesView />}
        {route.page === "work" && <RunsView mode={route.mode} onMode={(mode) => navigate({ page: "work", mode })} onRun={(id) => navigate({ page: "run-detail", id, mode: route.mode })} />}
        {route.page === "run-detail" && <RunDetailView id={route.id} onBack={() => navigate({ page: "work", mode: route.mode })} />}
        {route.page === "workers" && <WorkersView workers={workers.data} pending={workers.isPending} error={workers.error} fetching={workers.isFetching} updatedAt={workers.dataUpdatedAt} onWorker={(id) => navigate({ page: "worker", id })} onRefresh={() => void workers.refetch()} />}
        {route.page === "worker" && <WorkerDetail id={route.id} legacyReadOnly onBack={() => navigate({ page: "workers" })} onDelegate={() => {}} />}
        {route.page === "repositories" && <RepositoriesView onRepository={(id) => navigate({ page: "repository", id })} />}
        {route.page === "repository" && <RepositoryDetail id={route.id} onBack={() => navigate({ page: "repositories" })} />}
      </main>
    </div>
    {mobileNavOpen && <button className="nav-scrim" aria-label="Close navigation" onClick={() => setMobileNavOpen(false)} />}
  </div>;
}

function Nav({ active, icon, label, onClick }: { active: boolean; icon: ReactNode; label: string; onClick: () => void }) {
  return <button className={`nav-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined} onClick={onClick}>{icon}{label}</button>;
}

function pageTitle(route: Route): string {
  if (route.page === "run-detail") return "Run detail";
  if (route.page === "worker") return "Worker detail";
  if (route.page === "repository") return "Repository detail";
  return route.page[0].toUpperCase() + route.page.slice(1);
}
