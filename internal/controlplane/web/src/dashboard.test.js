import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
import {
  autoMergeRate, availableRepos, dailyTouchTrend, dayLabel, doneRate, formatCostPerPR,
  formatMedianCost, formatMillis, formatPercent, formatRatio, formatSpend, formatTokens, formatUSD,
  linePoints, mergeGateRiskParts, modelLabel, monthLabel, niceMax, runsOn, sortBySpend, sparkPoints,
  spendShare, statsPath, stepDays, stepSeries, touchParts, unreportedNote,
} from "./dashboard.js";

test("chart tops are round numbers above the data", () => {
  assert.equal(niceMax(0), 1);
  assert.equal(niceMax(1), 1);
  assert.equal(niceMax(3), 5);
  assert.equal(niceMax(7), 10);
  assert.equal(niceMax(12), 20);
  assert.equal(niceMax(240), 500);
  assert.equal(niceMax(0.35), 0.5);
  assert.equal(niceMax(1.4), 2);
});

test("numbers read plainly, and missing ones say so", () => {
  assert.equal(formatPercent(0.876), "88%");
  assert.equal(formatPercent(null), "None yet");
  assert.equal(formatMillis(125_000), "2m 5s");
  assert.equal(formatMillis(null), "None yet");
  assert.equal(formatTokens(950), "950");
  assert.equal(formatTokens(12_345), "12k");
  assert.equal(formatTokens(4_100_000), "4.1M");
});

test("spend formatting never presents unknown-cost harnesses as $0.00", () => {
  assert.equal(formatUSD(0.004), "$0.004");
  assert.equal(formatUSD(0.42), "$0.42");
  assert.equal(formatUSD(1234.5), "$1,234.50");
  assert.equal(formatUSD(null), "None yet");

  assert.equal(formatSpend({ cost: 12.5, has_cost: true, cost_unknown: 0 }), "$12.50");
  assert.equal(formatSpend({ cost: 12.5, has_cost: true, cost_unknown: 2 }), "$12.50+");
  assert.equal(formatSpend({ cost: 0, has_cost: false, cost_unknown: 3, tokens: 45_000 }), "Not reported");
  assert.equal(formatSpend({ cost: 0, has_cost: false, cost_unknown: 0, tokens: 12_000 }), "Not reported");
  assert.equal(formatSpend({ cost: 0, has_cost: false, cost_unknown: 0, tokens: 0 }), "None yet");

  assert.equal(formatMedianCost({ median_cost: 0.25, has_cost: true, cost_unknown: 0 }), "$0.25");
  assert.equal(formatMedianCost({ median_cost: 0.25, has_cost: true, cost_unknown: 1 }), "$0.25+");
  assert.equal(formatMedianCost({ median_cost: null, has_cost: false, cost_unknown: 2 }), "Not reported");
  assert.equal(formatMedianCost({ median_cost: null, has_cost: false, cost_unknown: 0 }), "None yet");

  assert.equal(formatCostPerPR(1.5, { mergedPRs: 2, costUnknown: 0 }), "$1.50");
  assert.equal(formatCostPerPR(1.5, { mergedPRs: 2, costUnknown: 1 }), "$1.50+");
  assert.equal(formatCostPerPR(null, { mergedPRs: 2, costUnknown: 2 }), "Not reported");
  assert.equal(formatCostPerPR(null, { mergedPRs: 0, costUnknown: 2 }), "None yet");

  assert.equal(unreportedNote(0), "");
  assert.equal(unreportedNote(1), "1 run unreported");
  assert.equal(unreportedNote(3, "step"), "3 steps unreported");
});

test("model, month, repo and spend helpers handle defaults cleanly", () => {
  assert.equal(monthLabel("2026-11"), "Nov 2026");
  assert.equal(monthLabel(""), "This month");
  assert.equal(modelLabel({ harness: "claude", model: "claude-opus-5-5" }), "claude-opus-5-5");
  assert.equal(modelLabel({ harness: "agy", model: "" }), "Antigravity default");
  assert.equal(spendShare(2, 8), 0.25);
  assert.ok(Number.isNaN(spendShare(0, 0)));
  assert.deepEqual(
    sortBySpend([{ name: "a", cost: 1, tokens: 50 }, { name: "b", cost: 4, tokens: 10 }, { name: "c", cost: 0, tokens: 90 }]).map(x => x.name),
    ["b", "a", "c"],
  );
  assert.deepEqual(dailyTouchTrend([{ merged: 2, touches: 3 }, { merged: 0, touches: 0 }, { merged: 1, touches: 2 }]), [1.5, 0, 2]);
  assert.equal(statsPath(14), "/api/v1/stats?days=14");
  assert.equal(statsPath(7, "owner/app"), "/api/v1/stats?days=7&repo=owner%2Fapp");
  assert.deepEqual(availableRepos({ repositories: ["owner/b", "owner/a"] }, ""), ["owner/a", "owner/b"]);
  assert.deepEqual(availableRepos({ repos: [{ name: "owner/only" }] }, "owner/selected"), ["owner/only", "owner/selected"]);
});

test("a day's runs add up its outcomes", () => {
  assert.equal(runsOn({ succeeded: 3, failed: 1, cancelled: 2 }), 6);
  assert.equal(dayLabel("2026-11-18"), "Nov 18");
});

test("done rate leaves out jobs still running", () => {
  assert.equal(doneRate({ jobs: { total: 5, active: 2, done: 2, failed: 1 } }), 2 / 3);
  assert.ok(Number.isNaN(doneRate({ jobs: { total: 1, active: 1 } })));
});

test("a sparkline spans its box, with the largest day at the top", () => {
  assert.deepEqual(sparkPoints([0, 5, 10], 100, 32), [[0, 32], [50, 16], [100, 0]]);
  assert.deepEqual(sparkPoints([0, 0], 100, 32), [[0, 32], [100, 32]], "an empty range lies flat on the floor");
  assert.deepEqual(sparkPoints([4], 100, 32), [], "one day has no shape to draw");
});

test("touches per pull request read as a short number", () => {
  assert.equal(formatRatio(0), "0");
  assert.equal(formatRatio(1.25), "1.3");
  assert.equal(formatRatio(3), "3");
  assert.equal(formatRatio(12.4), "12");
  assert.equal(formatRatio(null), "None yet");
  assert.equal(touchParts({ replies: 1, reruns: 2, reviews: 0 }), "1 reply, 2 reruns, 0 reviews");
});

test("each known step keeps its color, whichever ran most", () => {
  const busy = stepSeries(["review", "implement"]);
  const quiet = stepSeries(["implement", "review", "publish"]);
  assert.deepEqual(busy.map(({ label }) => label), ["Implement", "Review"]);
  assert.equal(busy[0].color, quiet[0].color);
  assert.equal(busy[1].color, quiet[1].color);
  assert.equal(quiet[2].color, "var(--chart-4)");
});

test("other steps take the free colors and the rest fold into Other", () => {
  const series = stepSeries(["implement", "plan", "test", "lint", "docs"]);
  assert.deepEqual(series.map(({ label }) => label), ["Implement", "Plan", "Test", "Lint", "Other"]);
  assert.equal(new Set(series.map(({ color }) => color)).size, 5);
  assert.deepEqual(series.at(-1).names, ["docs"]);
  const days = stepDays([{ day: "2026-11-18", steps: { implement: 2, docs: 1 } }, { day: "2026-11-19", steps: {} }], series);
  assert.deepEqual(days[0], { day: "2026-11-18", "step:implement": 2, "step:plan": 0, "step:test": 0, "step:lint": 0, "step:other": 1 });
  assert.equal(days[1]["step:implement"], 0);
  assert.deepEqual(stepSeries([]), []);
});

test("a line chart's points sit mid-column, with the top at the chart's top", () => {
  assert.deepEqual(linePoints([0, 5, 10], 10), [[16.67, 100], [50, 50], [83.33, 0]]);
  assert.deepEqual(linePoints([0, 0], 0), [[25, 100], [75, 100]], "an empty range lies on the floor");
});

test("merge gate helpers compute auto-merge rate and risk summary", () => {
  assert.equal(autoMergeRate({ evaluated: 8, auto_merged: 4 }), 0.5);
  assert.equal(autoMergeRate({ evaluated: 4, auto_merged: 0 }), 0);
  assert.ok(Number.isNaN(autoMergeRate({ evaluated: 0, auto_merged: 0 })));
  assert.ok(Number.isNaN(autoMergeRate(null)));

  assert.equal(mergeGateRiskParts({ evaluated: 8, low_risk: 5, medium_risk: 2, high_risk: 1 }), "5 low · 2 medium · 1 high risk");
  assert.equal(mergeGateRiskParts({ evaluated: 0, low_risk: 0, medium_risk: 0, high_risk: 0 }), "");
  assert.equal(mergeGateRiskParts(null), "");
});

test("Dashboard tabs show Overview, Models (with By step & cost_unknown), Spending, and filter by repo", { timeout: 30000 }, async context => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/dashboard" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "Event", "MouseEvent", "HTMLElement", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  const asked = [];
  const makeStats = (days, repo) => {
    const onlyUnmetered = repo === "owner/unmetered";
    const daily = Array.from({ length: days }, (_, index) => ({
      day: `2026-11-${String(index + 1).padStart(2, "0")}`,
      jobs: 1,
      succeeded: 1,
      failed: 0,
      cancelled: 0,
      opened: 1,
      merged: index === days - 1 ? 1 : 0,
      cost: onlyUnmetered ? 0 : (index === days - 1 ? 3.5 : 0.5),
      has_cost: !onlyUnmetered,
      tokens: 20_000,
      cost_unknown: index === days - 1 ? 1 : 0,
      touches: index === days - 1 ? 2 : 0,
      steps: { implement: 1, review: 1 },
    }));
    const claudeModel = {
      harness: "claude", model: "claude-opus-5-5", runs: 5, steps: 6, passed: 5, failed: 1, cancelled: 0,
      pass_rate: 5 / 6, opened_prs: 4, merged_prs: 3, merge_rate: 0.75,
      duration_millis: { median: 90_000, p90: 180_000 }, tokens: 120_000, cost: 6.0, has_cost: true,
      median_cost: 0.95, cost_unknown: 0, touches_per_pr: 1.5, rework_rate: 0.25,
    };
    const agyModel = {
      harness: "agy", model: "gemini-4-neon-high", runs: 3, steps: 4, passed: 4, failed: 0, cancelled: 0,
      pass_rate: 1, opened_prs: 3, merged_prs: 3, merge_rate: 1,
      duration_millis: { median: 45_000, p90: 60_000 }, tokens: 240_000, cost: 0, has_cost: false,
      median_cost: null, cost_unknown: 4, touches_per_pr: 0.5, rework_rate: 0,
    };
    const models = onlyUnmetered ? [agyModel] : [claudeModel, agyModel];
    const model_steps = onlyUnmetered
      ? [{ ...agyModel, step: "implement" }]
      : [
          { ...claudeModel, step: "implement", steps: 4, runs: 4 },
          { ...agyModel, step: "implement", steps: 2, runs: 2 },
          { ...agyModel, step: "review", steps: 2, runs: 2 },
        ];
    return {
      days,
      ...(repo ? { repo } : {}),
      repositories: ["owner/app", "owner/unmetered"],
      jobs: { total: 6, active: 0, done: 5, failed: 1, cancelled: 0 },
      runs: { total: 8, succeeded: 7, failed: 1, cancelled: 0 },
      success_rate: 7 / 8,
      run_millis: { median: 80_000, p90: 200_000 },
      wait_millis: { median: 3_000, p90: 10_000 },
      tokens: onlyUnmetered ? 240_000 : 360_000,
      cost: onlyUnmetered ? 0 : 6.0,
      has_cost: !onlyUnmetered,
      cost_unknown: onlyUnmetered ? 3 : 2,
      cost_per_merged_pr: onlyUnmetered ? null : 2.0,
      month_spend: {
        month: "2026-11",
        cost: onlyUnmetered ? 0 : 18.5,
        has_cost: !onlyUnmetered,
        tokens: onlyUnmetered ? 240_000 : 850_000,
        runs: onlyUnmetered ? 3 : 14,
        cost_unknown: onlyUnmetered ? 3 : 2,
        merged_prs: 3,
        cost_per_merged_pr: onlyUnmetered ? null : 6.17,
      },
      daily,
      presets: [{ name: "coding", jobs: { total: 6, active: 0, done: 5, failed: 1, cancelled: 0 }, run_millis: 80_000, runs: 8, tokens: onlyUnmetered ? 240_000 : 360_000, cost: onlyUnmetered ? 0 : 6.0, has_cost: !onlyUnmetered, cost_unknown: onlyUnmetered ? 3 : 2, merged_prs: 3 }],
      repos: onlyUnmetered
        ? [{ name: "owner/unmetered", jobs: { total: 3, active: 0, done: 3, failed: 0, cancelled: 0 }, run_millis: 45_000, runs: 3, tokens: 240_000, cost: 0, has_cost: false, cost_unknown: 3, merged_prs: 3 }]
        : [
            { name: "owner/app", jobs: { total: 3, active: 0, done: 2, failed: 1, cancelled: 0 }, run_millis: 90_000, runs: 5, tokens: 120_000, cost: 6.0, has_cost: true, cost_unknown: 0, merged_prs: 2 },
            { name: "owner/unmetered", jobs: { total: 3, active: 0, done: 3, failed: 0, cancelled: 0 }, run_millis: 45_000, runs: 3, tokens: 240_000, cost: 0, has_cost: false, cost_unknown: 3, merged_prs: 1 },
          ],
      sources: [{ name: "linear", jobs: { total: 6, active: 0, done: 5, failed: 1, cancelled: 0 }, run_millis: 80_000 }],
      models,
      model_steps,
      prs: { opened: 4, merged: 3, merge_rate: 0.75 },
      merge_gate: onlyUnmetered
        ? { evaluated: 0, approved: 0, auto_merged: 0, held_policy: 0, held_tripwire: 0, low_risk: 0, medium_risk: 0, high_risk: 0 }
        : { evaluated: 8, approved: 5, auto_merged: 4, held_policy: 2, held_tripwire: 1, low_risk: 5, medium_risk: 2, high_risk: 1 },
      cycle: { merged: 3, reviewed: 2, to_merge: { median: 3_600_000, p90: 7_200_000 }, to_pr: { median: 600_000, p90: 1_200_000 }, to_review: { median: 1_800_000, p90: 2_400_000 }, review_to_merge: { median: 1_200_000, p90: 1_800_000 } },
      touches: { replies: 2, reruns: 1, reviews: 3, per_pr: 2 },
      steps: ["implement", "review"],
    };
  };

  globalThis.fetch = async url => {
    asked.push(url);
    const parsed = new URL(url, "http://localhost");
    const days = Number(parsed.searchParams.get("days") || "14");
    const repo = parsed.searchParams.get("repo") || "";
    return { ok: true, status: 200, json: async () => makeStats(days, repo) };
  };
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;

  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });

  const { DashboardPage } = await server.ssrLoadModule("/src/dashboard-page.jsx");
  await act(async () => root.render(React.createElement(DashboardPage, { onSignedOut: () => {} })));

  // 1. Overview tab: Spend this month tile + Cost per merged PR + Human touches sparkline.
  await eventually(() => assert.ok(document.querySelector('[role="tabpanel"]#panel-overview')));
  assert.deepEqual([...document.querySelectorAll('[role="tab"]')].map(el => el.textContent), ["Overview", "Models", "Spending"]);
  const mtdTile = document.querySelector(".stat-tiles .stat-tile:nth-child(6)");
  assert.match(mtdTile.textContent, /Spend this month/);
  assert.match(mtdTile.textContent, /\$18\.50\+/);
  assert.match(mtdTile.textContent, /\$2\.00\+ \/ merged PR/);
  assert.match(mtdTile.textContent, /2 runs unreported/);
  const touchesTile = document.querySelector('[aria-labelledby="merge-heading"] .stat-tile:nth-child(5)');
  assert.ok(touchesTile.querySelector("svg.sparkline"), "Human touches per PR renders a daily sparkline");
  const mergeGateSection = document.querySelector('[aria-labelledby="merge-gate-heading"]');
  assert.ok(mergeGateSection, "Merge gate section renders when verdicts exist");
  assert.match(mergeGateSection.textContent, /Merge gate, for pull requests evaluated in the last 14 days/);
  assert.match(mergeGateSection.textContent, /5 low · 2 medium · 1 high risk/);
  const gateTiles = [...mergeGateSection.querySelectorAll(".stat-tile")];
  assert.equal(gateTiles.length, 4);
  assert.match(gateTiles[0].textContent, /Auto-merge rate.*50%.*4 of 8 evaluated PRs/);
  assert.match(gateTiles[1].textContent, /Approved \(low risk\).*5.*63% of evaluated · 4 auto-merged/);
  assert.match(gateTiles[2].textContent, /Held by policy.*2.*25% of evaluated/);
  assert.match(gateTiles[3].textContent, /Held by tripwire.*1.*13% of evaluated/);

  // 2. Models tab: compares claude-opus-5-5 vs gemini-4-neon-high, cost_unknown never shows $0.00.
  await act(async () => document.querySelector("#tab-models").click());
  await eventually(() => assert.ok(document.querySelector('[role="tabpanel"]#panel-models')));
  const modelRows = [...document.querySelectorAll("#panel-models tbody tr")];
  assert.equal(modelRows.length, 2);
  assert.match(modelRows[0].textContent, /claude-opus-5-5.*Claude Code.*5 \/ 6.*83%.*75%.*1m 30s.*\$0\.95.*\$6\.00.*120k.*1\.5.*25% rework/);
  assert.match(modelRows[1].textContent, /gemini-4-neon-high.*Antigravity.*3 \/ 4.*100%.*100%.*45s.*Not reported.*Not reported.*4 steps unreported.*240k.*0\.5/);
  assert.doesNotMatch(modelRows[1].textContent, /\$0\.00/, "unmetered harness must never display $0.00");

  // Toggle "By step" and filter to "review".
  await act(async () => [...document.querySelectorAll(".dashboard-toggle button")].find(b => b.textContent === "By step").click());
  await eventually(() => assert.equal(document.querySelectorAll("#panel-models tbody tr").length, 3));
  assert.ok(document.querySelector("#panel-models thead th.model-name-col")?.textContent === "Harness & model");
  await act(async () => [...document.querySelectorAll(".model-step-filters button")].find(b => b.textContent === "review").click());
  await eventually(() => assert.equal(document.querySelectorAll("#panel-models tbody tr").length, 1));
  assert.match(document.querySelector("#panel-models tbody tr").textContent, /review.*gemini-4-neon-high/);

  // 3. Spending tab: MTD, rolling-range, Cost per merged PR, Daily spend chart, Model/Repo/Preset tables.
  await act(async () => document.querySelector("#tab-spending").click());
  await eventually(() => assert.ok(document.querySelector('[role="tabpanel"]#panel-spending')));
  const spendText = document.querySelector("#panel-spending").textContent;
  assert.match(spendText, /Total spend this month.*\$18\.50\+.*Nov 2026/);
  assert.match(spendText, /Spend \(14 days\).*\$6\.00\+/);
  assert.match(spendText, /Cost per merged PR.*\$2\.00\+/);
  assert.ok(document.querySelector('[aria-labelledby="spend-chart"]'));
  assert.ok(document.querySelector('[aria-labelledby="spend-models-table"]'));
  assert.ok(document.querySelector('[aria-labelledby="spend-repos-table"]')?.classList.contains("spend-group-table"));
  assert.ok(document.querySelector('[aria-labelledby="spend-presets-table"]')?.classList.contains("spend-group-table"));

  // Hovering the last column of Daily spend shows reported spend + unreported runs.
  const spendCols = document.querySelectorAll('[aria-labelledby="spend-chart"] .chart-column');
  await act(async () => spendCols[spendCols.length - 1].dispatchEvent(new window.MouseEvent("mouseover", { bubbles: true })));
  await eventually(() => assert.match(document.querySelector('[aria-labelledby="spend-chart"] .chart-tooltip')?.textContent || "", /\$3\.50\+.*Tokens 20k.*Unreported 1 run/));

  // 4. Repository filter sends ?repo= and purely unmetered repo never displays $0.00.
  const repoSelect = document.querySelector('select[aria-label="Repository"]');
  assert.deepEqual([...repoSelect.options].map(o => o.value), ["", "owner/app", "owner/unmetered"]);
  await act(async () => {
    repoSelect.value = "owner/unmetered";
    repoSelect.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
  await eventually(() => assert.ok(asked.includes("/api/v1/stats?days=14&repo=owner%2Funmetered")));
  await eventually(() => assert.match(document.querySelector("#panel-spending .stat-tiles").textContent, /Not reported/));
  assert.doesNotMatch(document.querySelector("#panel-spending .stat-tiles").textContent, /\$0\.00/);
  assert.deepEqual([...document.querySelector('select[aria-label="Repository"]').options].map(o => o.value), ["", "owner/app", "owner/unmetered"]);

  // Switching back to Overview for a repo with zero merge-gate evaluations hides the Merge gate section.
  await act(async () => document.querySelector("#tab-overview").click());
  await eventually(() => assert.ok(document.querySelector('[role="tabpanel"]#panel-overview')));
  assert.equal(document.querySelector('[aria-labelledby="merge-gate-heading"]'), null);
});

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}
