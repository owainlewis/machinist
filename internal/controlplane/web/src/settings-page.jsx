import React, { useEffect } from "react";
import { cn } from "@/lib/utils";
import { settingsHref } from "./routes.js";
import { settingsPages } from "./settings.js";
import { missingSteps } from "./setup.js";
import { OverviewPage, ProjectsPage } from "./setup-page.jsx";
import { GitHubPage, LinearPage, PresetsPage, TokensPage } from "./settings-pages.jsx";

const pages = { overview: OverviewPage, projects: ProjectsPage, github: GitHubPage, linear: LinearPage, tokens: TokensPage, presets: PresetsPage };

// SettingsPage shows how Machinist is configured, one topic a page, beside a
// side nav. config.yaml and the factory files stay the source of truth, so
// only projects and tokens change here.
export function SettingsPage({ page, setup, onSignedOut, onSetupChange }) {
  const Page = pages[page] || OverviewPage;
  const missing = missingSteps(setup).length > 0;
  // At phone width the side nav is a row of tabs that scrolls, so the open
  // page's tab is scrolled into view.
  useEffect(() => {
    document.querySelector(".settings-nav-item-active")?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [page]);
  return <div className="page settings-page">
    <nav className="settings-nav" aria-label="Settings">
      <span className="label settings-nav-title">Settings</span>
      {settingsPages.map(([name, label]) => <a key={name} href={settingsHref(name)} aria-current={name === page ? "page" : undefined} className={cn("settings-nav-item", name === page && "settings-nav-item-active")}>
        {label}{name === "overview" && missing && <span className="nav-dot" aria-label="Setup needs attention" />}
      </a>)}
    </nav>
    <div className="settings-body">
      <Page key={page} onSignedOut={onSignedOut} onChange={onSetupChange} />
    </div>
  </div>;
}
