import React, { useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import { Clock3, House, LogOut, Moon, Rows3, Server, Sun } from "lucide-react";
import { cn } from "@/lib/utils";
import { routeFromHash } from "@/routes";
import { createStatusLoader } from "@/status-loader";
import { api, SignedOutError } from "./api.js";
import { AutomationsPage } from "./automations.jsx";
import { HomePage } from "./home.jsx";
import { RunnersPage } from "./runners.jsx";
import { SignIn } from "./sign-in.jsx";
import { TaskDetail } from "./task-detail.jsx";
import { WorkPage } from "./work.jsx";
import "./styles.css";

const pollMillis = 2000;
const emptyStatus = { tickets: [], runners: [], queue: null, automations: [], ledger: null };
const navigation = [["home", House, "Home"], ["work", Rows3, "Work"], ["automations", Clock3, "Automations"], ["runners", Server, "Runners"]];

async function loadStatus() {
  const [tickets, runners, automations, ledger] = await Promise.all([api("/api/v1/tickets"), api("/api/v1/runners"), api("/api/v1/automations"), api("/api/v1/ledger")]);
  return { tickets: tickets.tickets, runners: runners.runners, queue: runners.queue, automations: automations.automations, ledger };
}

function readTheme() {
  try { return localStorage.getItem("machinist-theme") !== "light"; } catch { return true; }
}

function App() {
  const [session, setSession] = useState({ checked: false, signedIn: false, repositories: [] });
  const [status, setStatus] = useState(emptyStatus);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [dark, setDark] = useState(readTheme);
  const [route, setRoute] = useState(() => routeFromHash(window.location.hash));
  const signedOut = useCallback(() => {
    setSession({ checked: true, signedIn: false, repositories: [] });
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
      const { repositories } = await api("/api/v1/repositories");
      setSession({ checked: true, signedIn: true, repositories });
    } catch (failure) {
      if (failure instanceof SignedOutError) signedOut();
      else { setSession({ checked: true, signedIn: false, repositories: [] }); setError(failure.message); }
    }
  }, [signedOut]);
  useEffect(() => { checkSession(); }, [checkSession]);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    try { localStorage.setItem("machinist-theme", dark ? "dark" : "light"); } catch { /* The theme lasts for this visit. */ }
  }, [dark]);

  useEffect(() => {
    const update = () => setRoute(routeFromHash(window.location.hash));
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
  const refresh = () => loader.current.refresh();
  const active = route.view === "ticket" ? "work" : route.view;
  return <div className="app-shell min-h-screen bg-background text-foreground md:flex">
    <aside className="app-sidebar sticky top-0 z-20 flex shrink-0 items-center border-b border-border bg-sidebar px-3 py-2 md:h-screen md:w-52 md:flex-col md:items-stretch md:border-b-0 md:border-r md:px-3 md:py-4">
      <div className="flex h-9 items-center px-2"><span className="brand-wordmark">Machinist</span></div>
      <nav className="primary-nav md:mt-4" aria-label="Primary">
        {navigation.map(([page, Icon, label]) => <a key={page} href={`#/${page}`} aria-current={active === page ? "page" : undefined} className={cn("nav-item", active === page && "nav-item-active")}><Icon className="size-4" aria-hidden="true" /><span>{label}</span></a>)}
      </nav>
      <div className="ml-auto flex items-center gap-1 md:mt-auto md:ml-0">
        <button type="button" className="icon-button" onClick={() => setDark(value => !value)} aria-label={dark ? "Use light theme" : "Use dark theme"}>{dark ? <Sun className="size-4" /> : <Moon className="size-4" />}</button>
        <button type="button" className="icon-button" onClick={signOut} aria-label="Sign out"><LogOut className="size-4" /></button>
      </div>
    </aside>
    <main className="min-w-0 flex-1">
      {route.view === "home" && <HomePage repositories={session.repositories} tickets={status.tickets} runners={status.runners} queue={status.queue} loaded={loaded} error={error} onChanged={refresh} />}
      {route.view === "work" && <WorkPage tickets={status.tickets} ledger={status.ledger} loaded={loaded} error={error} />}
      {route.view === "ticket" && <TaskDetail ticketID={route.ticketID} onSignedOut={signedOut} onChanged={refresh} />}
      {route.view === "automations" && <AutomationsPage automations={status.automations} loaded={loaded} error={error} onChanged={refresh} />}
      {route.view === "runners" && <RunnersPage runners={status.runners} queue={status.queue} loaded={loaded} error={error} />}
    </main>
  </div>;
}

export const appRoot = createRoot(document.getElementById("root"));
appRoot.render(<App />);
