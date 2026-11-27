import React, { useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import { CalendarClock, ChartColumn, LogOut, Rows3, Server, Settings2 } from "lucide-react";
import { Brand } from "@/components/ui/brand";
import { cn } from "@/lib/utils";
import { redirectFor, routeFromHash, settingsHref } from "@/routes";
import { createStatusLoader } from "@/status-loader";
import { api, SignedOutError } from "./api.js";
import { AutomationsPage } from "./automations-view.jsx";
import { DashboardPage } from "./dashboard-page.jsx";
import { JobPage } from "./job-page.jsx";
import { JobsPage } from "./jobs-page.jsx";
import { NewJobPage } from "./new-job-page.jsx";
import { RunnersPage } from "./runners.jsx";
import { defaultSettingsPage } from "./settings.js";
import { settingsNeedAttention, shouldOpenSetup } from "./setup.js";
import { SettingsPage } from "./settings-page.jsx";
import { clearSettingsCache } from "./settings-pages.jsx";
import { SignIn } from "./sign-in.jsx";
import "./styles.css";

const pollMillis = 2000;
const emptyStatus = { runners: [], queue: null };
const defaultAuthStatus = { methods: [], token_signin_enabled: true };
const navigation = [["dashboard", ChartColumn, "Dashboard"], ["jobs", Rows3, "Jobs"], ["automations", CalendarClock, "Automations"], ["runners", Server, "Runners"], ["settings", Settings2, "Settings"]];

// followRedirect moves an old address, such as #/setup, to where it lives now,
// in place, so Back does not return to it. It returns the hash to show.
function followRedirect(role = "admin") {
  const moved = redirectFor(window.location.hash, role);
  if (moved) window.history.replaceState(null, "", moved);
  return window.location.hash;
}

async function loadStatus() {
  const runners = await api("/api/v1/runners");
  return { runners: runners.runners, queue: runners.queue };
}

function App() {
  const [session, setSession] = useState({ checked: false, signedIn: false, me: null });
  const [authStatus, setAuthStatus] = useState(defaultAuthStatus);
  const [status, setStatus] = useState(emptyStatus);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const role = session.me?.role || "admin";
  const isAdmin = role === "admin";
  const [route, setRoute] = useState(() => routeFromHash(followRedirect("admin"), "admin"));
  const [setup, setSetup] = useState(null);
  const signedOut = useCallback(() => {
    clearSettingsCache();
    setSetup(null);
    setSession({ checked: true, signedIn: false, me: null });
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
      const found = await api("/api/v1/auth/status");
      setAuthStatus({
        methods: Array.isArray(found.methods) ? found.methods : [],
        token_signin_enabled: Boolean(found.token_signin_enabled),
      });
      if (!found.authenticated) {
        signedOut();
        return;
      }
      const me = await api("/api/v1/me");
      const nextRole = me?.role || "admin";
      setSession({ checked: true, signedIn: true, me });
      setRoute(routeFromHash(followRedirect(nextRole), nextRole));
    } catch (failure) {
      if (failure instanceof SignedOutError) signedOut();
      else { setSession({ checked: true, signedIn: false, me: null }); setError(failure.message); }
    }
  }, [signedOut]);
  useEffect(() => { checkSession(); }, [checkSession]);

  useEffect(() => {
    const update = () => {
      setRoute(routeFromHash(followRedirect(role), role));
      document.documentElement.scrollTop = 0;
    };
    window.addEventListener("hashchange", update);
    return () => window.removeEventListener("hashchange", update);
  }, [role]);

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

  // A new install opens on the setup checklist until every required step is done.
  // Only admins can read /api/v1/setup.
  useEffect(() => {
    if (!session.signedIn || !isAdmin) return;
    let cancelled = false;
    api("/api/v1/setup").then(found => {
      if (cancelled) return;
      setSetup(found);
      if (shouldOpenSetup(found, window.location.hash)) window.location.hash = settingsHref("overview");
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [session.signedIn, isAdmin]);

  async function signOut() {
    await api("/api/v1/session", { method: "DELETE" }).catch(() => {});
    signedOut();
  }

  if (!session.checked) return null;
  if (!session.signedIn) return <SignIn authStatus={authStatus} onSignedIn={checkSession} />;
  const active = route.view === "job" || route.view === "new" ? "jobs" : route.view;
  const user = session.me?.user;
  const handle = user ? `@${user.github_login}` : session.me?.token_name;

  return <div className="app-shell">
    <aside className="app-sidebar">
      <Brand />
      <nav className="primary-nav" aria-label="Primary">
        {navigation.map(([page, Icon, label]) => <a key={page} href={page === "settings" ? settingsHref(defaultSettingsPage(role)) : `#/${page}`} aria-current={active === page ? "page" : undefined} className={cn("nav-item", active === page && "nav-item-active")}><Icon aria-hidden="true" /><span>{label}</span>{page === "settings" && isAdmin && settingsNeedAttention(setup) && <span className="nav-dot" aria-label="Settings need attention" />}</a>)}
      </nav>
      <div className="sidebar-foot">
        {handle && <a className="sidebar-user" href={settingsHref("account")}>
          {user?.avatar_url && <img className="sidebar-avatar" src={user.avatar_url} alt="" width="20" height="20" />}
          <span className="sidebar-user-name">{handle}</span>
          <span className="sidebar-user-role">{role}</span>
        </a>}
        <button type="button" className="nav-item" onClick={signOut} aria-label="Sign out"><LogOut aria-hidden="true" /><span>Sign out</span></button>
      </div>
    </aside>
    <main className="app-main">
      {route.view === "jobs" && <JobsPage runners={status.runners} role={role} onSignedOut={signedOut} />}
      {route.view === "automations" && <AutomationsPage runners={status.runners} role={role} onSignedOut={signedOut} />}
      {route.view === "new" && role !== "viewer" && <NewJobPage onSignedOut={signedOut} />}
      {route.view === "job" && <JobPage key={route.jobID} jobID={route.jobID} role={role} onSignedOut={signedOut} />}
      {route.view === "dashboard" && <DashboardPage onSignedOut={signedOut} />}
      {route.view === "settings" && <SettingsPage page={route.page} setup={setup} me={session.me} role={role} onSignedOut={signedOut} onSetupChange={setSetup} />}
      {route.view === "runners" && <RunnersPage runners={status.runners} queue={status.queue} loaded={loaded} error={error} role={role} onChange={() => loader.current.refresh()} onSignedOut={signedOut} />}
    </main>
  </div>;
}

export const appRoot = createRoot(document.getElementById("root"));
appRoot.render(<App />);
