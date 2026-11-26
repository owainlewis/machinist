import React, { useEffect } from "react";
import { cn } from "@/lib/utils";
import { settingsHref } from "./routes.js";
import { defaultSettingsPage, visibleSettingsPages } from "./settings.js";
import { factoryUpdates, missingSteps } from "./setup.js";
import { OverviewPage, ProjectsPage } from "./setup-page.jsx";
import { GitHubPage, LinearPage, PresetsPage, TokensPage } from "./settings-pages.jsx";
import { AccountPage, TeamPage } from "./team-pages.jsx";

const pages = {
  overview: OverviewPage,
  projects: ProjectsPage,
  github: GitHubPage,
  linear: LinearPage,
  tokens: TokensPage,
  team: TeamPage,
  presets: PresetsPage,
  account: AccountPage,
};

// SettingsPage shows how Machinist is configured, one topic a page, beside a
// side nav. Non-admins only see Presets and My Account.
export function SettingsPage({ page, setup, me, role = "admin", onSignedOut, onSetupChange }) {
  const allowed = visibleSettingsPages(role);
  const allowedSet = new Set(allowed.map(([name]) => name));
  const resolvedPage = allowedSet.has(page) ? page : defaultSettingsPage(role);
  const Page = pages[resolvedPage] || AccountPage;
  const isAdmin = role === "admin";
  const missing = isAdmin && missingSteps(setup).length > 0;
  const outdatedFactory = isAdmin && factoryUpdates(setup).length > 0;

  useEffect(() => {
    document.querySelector(".settings-nav-item-active")?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [resolvedPage]);

  return <div className="page settings-page">
    <nav className="settings-nav" aria-label="Settings">
      <span className="label settings-nav-title">Settings</span>
      {allowed.map(([name, label]) => <a key={name} href={settingsHref(name)} aria-current={name === resolvedPage ? "page" : undefined} className={cn("settings-nav-item", name === resolvedPage && "settings-nav-item-active")}>
        {label}
        {name === "overview" && missing && <span className="nav-dot" aria-label="Setup needs attention" />}
        {name === "presets" && outdatedFactory && <span className="nav-dot" aria-label="Factory files are out of date" />}
      </a>)}
    </nav>
    <div className="settings-body">
      <Page key={resolvedPage} setup={setup} me={me} onSignedOut={onSignedOut} onChange={onSetupChange} />
    </div>
  </div>;
}
