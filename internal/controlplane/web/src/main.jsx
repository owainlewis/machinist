import React, { useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import { ChartColumn, LogOut, Moon, Rows3, Server, Sun } from "lucide-react";
import { Brand } from "@/components/ui/brand";
import { cn } from "@/lib/utils";
import { routeFromHash } from "@/routes";
import { createStatusLoader } from "@/status-loader";
import { api, SignedOutError } from "./api.js";
import { DashboardPage } from "./dashboard-page.jsx";
import { JobPage } from "./job-page.jsx";
import { JobsPage } from "./jobs-page.jsx";
import { NewJobPage } from "./new-job-page.jsx";
import { RunnersPage } from "./runners.jsx";
import { SignIn } from "./sign-in.jsx";
import "./styles.css";

const pollMillis = 2000;
const emptyStatus = { tickets: [], runners: [], queue: null };
const navigation = [["jobs", Rows3, "Jobs"], ["dashboard", ChartColumn, "Dashboard"], ["runners", Server, "Runners"]];

async function loadStatus() {
  const [tickets, runners] = await Promise.all([api("/api/v1/tickets"), api("/api/v1/runners")]);
  return { tickets: tickets.tickets, runners: runners.runners, queue: runners.queue };
}

// A theme someone chose wins. Until they choose, the app follows the system.
function readTheme() {
  try {
    const stored = localStorage.getItem("machinist-theme");
    if (stored) return stored === "dark";
  } catch { /* Storage is off, so the system decides. */ }
  return Boolean(window.matchMedia?.("(prefers-color-scheme: dark)").matches);
}

function App() {
  const [session, setSession] = useState({ checked: false, signedIn: false });
  const [status, setStatus] = useState(emptyStatus);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [dark, setDark] = useState(readTheme);
  const [route, setRoute] = useState(() => routeFromHash(window.location.hash));
  const signedOut = useCallback(() => {
    setSession({ checked: true, signedIn: false });
    setStatus(emptyStatus);
    setLoaded(false);
  }, []);
  const loader = useRef(null);
  if (!loader.current) loader.current = createStatusLoader({
    request: loadStatus,
    apply: result => {
      if (result.kind === "success") {
        setStatus(result.status);
        setError("");
        setLoaded(true);
      } else if (result.message === new SignedOutError().message) signedOut();
      else setError(result.message);
    },
  });

  const checkSession = useCallback(async () => {
    try {
      await api("/api/v1/repositories");
      setSession({ checked: true, signedIn: true });
    } catch (failure) {
      if (failure instanceof SignedOutError) signedOut();
      else { setSession({ checked: true, signedIn: false }); setError(failure.message); }
    }
  }, [signedOut]);
  useEffect(() => { checkSession(); }, [checkSession]);

  useEffect(() => { document.documentElement.classList.toggle("dark", dark); }, [dark]);
  function switchTheme() {
    try { localStorage.setItem("machinist-theme", dark ? "light" : "dark"); } catch { /* The theme lasts for this visit. */ }
    setDark(!dark);
  }

  useEffect(() => {
    const update = () => {
      setRoute(routeFromHash(window.location.hash));
      document.documentElement.scrollTop = 0;
    };
    window.addEventListener("hashchange", update);
    return () => window.removeEventListener("hashchange", update);
  }, []);

  useEffect(() => {
    if (!session.signedIn) return undefined;
    let stopped = false;
    let timer;
    const load = async () => {
      await loader.current.refresh();
      if (!stopped) timer = window.setTimeout(load, pollMillis);
    };
    load();
    return () => { stopped = true; loader.current.cancel(); window.clearTimeout(timer); };
  }, [session.signedIn]);

  async function signOut() {
    await api("/api/v1/session", { method: "DELETE" }).catch(() => {});
    signedOut();
  }

  if (!session.checked) return null;
  if (!session.signedIn) return <SignIn onSignedIn={checkSession} />;
  const active = route.view === "job" || route.view === "new" ? "jobs" : route.view;
  const ThemeIcon = dark ? Sun : Moon;
  const themeLabel = dark ? "Light mode" : "Dark mode";
  return <div className="app-shell">
    <aside className="app-sidebar">
      <Brand />
      <nav className="primary-nav" aria-label="Primary">
        {navigation.map(([page, Icon, label]) => <a key={page} href={`#/${page}`} aria-current={active === page ? "page" : undefined} className={cn("nav-item", active === page && "nav-item-active")}><Icon aria-hidden="true" /><span>{label}</span></a>)}
      </nav>
      <div className="sidebar-foot">
        <button type="button" className="nav-item" onClick={switchTheme} aria-label={themeLabel}><ThemeIcon aria-hidden="true" /><span>{themeLabel}</span></button>
        <button type="button" className="nav-item" onClick={signOut} aria-label="Sign out"><LogOut aria-hidden="true" /><span>Sign out</span></button>
      </div>
    </aside>
    <main className="app-main">
      {route.view === "jobs" && <JobsPage tickets={status.tickets} runners={status.runners} loaded={loaded} error={error} />}
      {route.view === "new" && <NewJobPage onSignedOut={signedOut} />}
      {route.view === "job" && <JobPage key={route.jobID} jobID={route.jobID} onSignedOut={signedOut} />}
      {route.view === "dashboard" && <DashboardPage onSignedOut={signedOut} />}
      {route.view === "runners" && <RunnersPage runners={status.runners} queue={status.queue} loaded={loaded} error={error} />}
    </main>
  </div>;
}

export const appRoot = createRoot(document.getElementById("root"));
appRoot.render(<App />);
