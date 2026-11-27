import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

function makeTicket(index, { state = "done", source = "api", minutesAgo = index } = {}) {
  const time = new Date(Date.UTC(2026, 10, 24, 12, 0, 0) - minutesAgo * 60_000).toISOString();
  return {
    id: `t_${String(index).padStart(4, "0")}`,
    title: `Job #${index}`,
    repo: "owner/repo",
    agent: "default",
    state,
    source,
    created_at: time,
    updated_at: time,
  };
}

function setupDOM(initialView = "board") {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/jobs" });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "HTMLElement", "MouseEvent", "Event", "localStorage", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  dom.window.localStorage.setItem("machinist-jobs-view", initialView);
  return {
    dom,
    cleanup() {
      dom.window.close();
      for (const [name, descriptor] of previous) {
        if (descriptor) Object.defineProperty(globalThis, name, descriptor);
        else delete globalThis[name];
      }
    },
  };
}

async function eventually(check, attempts = 60) {
  for (let i = 0; i < attempts; i++) {
    try { check(); return; } catch (error) { if (i === attempts - 1) throw error; }
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
  }
}

test("Board caps Done to 20 cards, keeps Queued and Running uncapped, and jumps to paginated List past 200 jobs", { timeout: 30000 }, async context => {
  const { dom, cleanup } = setupDOM("board");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });

  // 23 active jobs (12 queued + 11 running) + 215 finished jobs = 238 total (> 200 cliff).
  const queued = Array.from({ length: 12 }, (_, i) => makeTicket(i + 1, { state: "working", source: "web", minutesAgo: 400 + i }));
  const running = Array.from({ length: 11 }, (_, i) => makeTicket(100 + i, { state: "working", source: "linear", minutesAgo: 300 + i }));
  const finished = Array.from({ length: 215 }, (_, i) => makeTicket(1000 + i, {
    state: i % 5 === 0 ? "failed" : "done",
    source: i % 2 === 0 ? "linear" : "api",
    minutesAgo: i,
  }));
  const all = [...finished, ...running, ...queued];
  const runningSet = new Set(running.map(ticket => ticket.id));
  const runners = [{ name: "runner-1", jobs: running.map(ticket => ({ ticket_id: ticket.id, step: "work" })) }];
  const requests = [];

  globalThis.fetch = async url => {
    requests.push(url);
    const parsed = new URL(url, "http://localhost");
    const source = parsed.searchParams.get("source") || "";
    const stateParam = parsed.searchParams.get("state") || "";
    const view = parsed.searchParams.get("view") || "";

    const matchesSource = ticket => !source || ticket.source === source;
    const states = stateParam ? stateParam.split(",") : [];
    const matchesState = ticket => {
      if (!states.length) return true;
      return states.some(st => {
        if (st === "running") return ticket.state === "working" && runningSet.has(ticket.id);
        if (st === "queued") return ticket.state === "working" && !runningSet.has(ticket.id);
        return ticket.state === st;
      });
    };

    const bySource = all.filter(matchesSource);
    const counts = {
      queued: bySource.filter(t => t.state === "working" && !runningSet.has(t.id)).length,
      running: bySource.filter(t => t.state === "working" && runningSet.has(t.id)).length,
      done: bySource.filter(t => t.state === "done" || t.state === "pr_ready").length,
      failed: bySource.filter(t => t.state === "failed").length,
      needs_person: bySource.filter(t => t.state === "pr_ready").length,
      cancelled: bySource.filter(t => t.state === "cancelled").length,
    };
    const sources = ["api", "linear", "web"];
    const matching = bySource.filter(matchesState);

    if (view === "board") {
      const doneLimit = Number(parsed.searchParams.get("done_limit") || 20);
      const active = matching.filter(t => t.state === "working" || t.state === "pr_ready");
      const done = matching.filter(t => t.state !== "working" && t.state !== "pr_ready");
      const capped = done.slice(0, doneLimit);
      return {
        ok: true,
        status: 200,
        json: async () => ({
          tickets: [...active, ...capped],
          total: active.length + done.length,
          limit: doneLimit,
          offset: 0,
          has_more: done.length > capped.length,
          done_total: done.length,
          done_limit: doneLimit,
          counts,
          sources,
        }),
      };
    }

    const limit = Number(parsed.searchParams.get("limit") || 50);
    const offset = Number(parsed.searchParams.get("offset") || 0);
    const page = matching.slice(offset, offset + limit);
    return {
      ok: true,
      status: 200,
      json: async () => ({
        tickets: page,
        total: matching.length,
        limit,
        offset,
        has_more: offset + page.length < matching.length,
        counts,
        sources,
      }),
    };
  };

  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  await act(async () => root.render(React.createElement(JobsPage, { runners, onSignedOut() {} })));

  // Board shows all 12 Queued, all 11 Running, and caps Done to 20 cards while showing total 215.
  await eventually(() => assert.equal(document.querySelectorAll('[aria-labelledby="board-done"] .board-card').length, 20));
  assert.equal(document.querySelectorAll('[aria-labelledby="board-queued"] .board-card').length, 12, "all Queued jobs stay visible");
  assert.equal(document.querySelectorAll('[aria-labelledby="board-running"] .board-card').length, 11, "all Running jobs stay visible");
  assert.equal(document.querySelector('#board-done .board-count').textContent, "215");
  assert.match(document.querySelector(".board-column-summary").textContent, /Showing 20 of 215/);

  // Footer button switches to List view and fetches page 1 (limit=50&offset=0).
  const viewAll = document.querySelector(".board-view-all");
  assert.equal(viewAll.textContent, "View all in List");
  await act(async () => viewAll.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));

  await eventually(() => assert.equal(document.querySelectorAll("table.jobs tbody tr").length, 50));
  assert.equal(dom.window.localStorage.getItem("machinist-jobs-view"), "list");
  assert.equal(document.querySelector(".pagination-summary").textContent, "Showing 1–50 of 238");
  assert.equal(document.querySelector(".pagination-page").textContent, "Page 1 of 5");

  const prevButton = () => [...document.querySelectorAll(".pagination button")].find(btn => btn.textContent.includes("Previous"));
  const nextButton = () => [...document.querySelectorAll(".pagination button")].find(btn => btn.textContent.includes("Next"));
  assert.equal(prevButton().disabled, true);
  assert.equal(nextButton().disabled, false);

  // Navigate to Page 5 (offset 200), proving rows beyond 200 are browsable.
  for (let expectedPage = 2; expectedPage <= 5; expectedPage++) {
    await act(async () => nextButton().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
    await eventually(() => assert.equal(document.querySelector(".pagination-page").textContent, `Page ${expectedPage} of 5`));
  }
  assert.equal(document.querySelector(".pagination-summary").textContent, "Showing 201–238 of 238");
  assert.equal(document.querySelectorAll("table.jobs tbody tr").length, 38);
  assert.equal(prevButton().disabled, false);
  assert.equal(nextButton().disabled, true);
  assert.ok(requests.some(url => url.includes("limit=50&offset=200")), `expected offset=200 request, got ${requests.join(", ")}`);

  // Step back to Page 4.
  await act(async () => prevButton().dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.equal(document.querySelector(".pagination-page").textContent, "Page 4 of 5"));
  assert.equal(document.querySelector(".pagination-summary").textContent, "Showing 151–200 of 238");

  // Applying a quick filter resets to Page 1 and queries with state=failed.
  const failedChip = document.querySelector('.filter-chip[data-filter="failed"]');
  assert.equal(failedChip.querySelector(".filter-chip-count").textContent, "43", "chip counts span all 238 jobs");
  await act(async () => failedChip.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  await eventually(() => assert.equal(document.querySelectorAll("table.jobs tbody tr").length, 43));
  assert.equal(document.querySelector(".pagination"), null, "single-page results hide pagination controls");
  assert.ok(requests.some(url => url.includes("state=failed&limit=50&offset=0")));
});

test("Board cards and List rows show Low, Medium, and High merge-gate risk badges and omit them when absent", { timeout: 30000 }, async context => {
  const { dom, cleanup } = setupDOM("board");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });

  const tickets = [
    {
      ...makeTicket(1, { state: "done", source: "linear", minutesAgo: 1 }),
      title: "Auto-merged low risk",
      pr_number: 101,
      pr_state: "merged",
      merge_gate_decision: "approve",
      merge_gate_risk: "low",
      merge_gate_reason: "Small docs update covered by tests.",
    },
    {
      ...makeTicket(2, { state: "pr_ready", source: "linear", minutesAgo: 2 }),
      title: "Held medium risk",
      pr_number: 102,
      pr_state: "open",
      merge_gate_decision: "approve",
      merge_gate_risk: "medium",
      merge_gate_reason: "Changes API response schema.",
    },
    {
      ...makeTicket(3, { state: "pr_ready", source: "api", minutesAgo: 3 }),
      title: "Held high risk",
      pr_number: 103,
      pr_state: "open",
      merge_gate_decision: "human_review",
      merge_gate_risk: "high",
      merge_gate_reason: "Alters authentication token handling.",
    },
    {
      ...makeTicket(4, { state: "working", source: "web", minutesAgo: 4 }),
      title: "Plain queued job",
    },
    {
      ...makeTicket(5, { state: "pr_ready", source: "api", minutesAgo: 5 }),
      title: "Legacy PR without merge gate",
      pr_number: 105,
      pr_state: "open",
    },
  ];

  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  await act(async () => root.render(React.createElement(JobsPage, { tickets, runners: [], loaded: true })));

  const cardByTitle = title => [...document.querySelectorAll(".board-card")].find(card => card.querySelector(".board-card-title")?.textContent === title);

  const lowCard = cardByTitle("Auto-merged low risk");
  assert.ok(lowCard.querySelector(".board-card-foot .pr-chip"), "PR chip stays in card footer");
  const lowBadge = lowCard.querySelector('.board-card-foot .risk-badge[data-risk="low"]');
  assert.ok(lowBadge, "expected Low risk badge in .board-card-foot");
  assert.equal(lowBadge.textContent, "Low");
  assert.match(lowBadge.getAttribute("title"), /Small docs update covered by tests\./);

  const mediumCard = cardByTitle("Held medium risk");
  assert.ok(mediumCard.querySelector(".board-card-foot .pr-chip"));
  const mediumBadge = mediumCard.querySelector('.board-card-foot .risk-badge[data-risk="medium"]');
  assert.ok(mediumBadge, "expected Medium risk badge in .board-card-foot");
  assert.equal(mediumBadge.textContent, "Medium");

  const highCard = cardByTitle("Held high risk");
  assert.ok(highCard.querySelector(".board-card-foot .pr-chip"));
  const highBadge = highCard.querySelector('.board-card-foot .risk-badge[data-risk="high"]');
  assert.ok(highBadge, "expected High risk badge in .board-card-foot");
  assert.equal(highBadge.textContent, "High");

  const plainCard = cardByTitle("Plain queued job");
  assert.equal(plainCard.querySelector(".board-card-foot"), null, "queued card without PR or risk keeps no footer");
  assert.equal(plainCard.querySelector(".risk-badge"), null);

  const legacyCard = cardByTitle("Legacy PR without merge gate");
  assert.ok(legacyCard.querySelector(".board-card-foot .pr-chip"));
  assert.equal(legacyCard.querySelector(".risk-badge"), null, "card without merge_gate_risk shows no risk badge");

  // Switch to List view and verify the Risk column renders matching badges cleanly.
  const listButton = [...document.querySelectorAll(".range button")].find(button => button.textContent.includes("List"));
  await act(async () => listButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));

  assert.equal(document.querySelector("table.jobs thead th.job-risk")?.textContent, "Risk");
  const rowByTitle = title => [...document.querySelectorAll("table.jobs tbody tr")].find(row => row.querySelector(".job-title")?.textContent === title);

  assert.equal(rowByTitle("Auto-merged low risk").querySelector('.job-risk .risk-badge[data-risk="low"]')?.textContent, "Low");
  assert.equal(rowByTitle("Held medium risk").querySelector('.job-risk .risk-badge[data-risk="medium"]')?.textContent, "Medium");
  assert.equal(rowByTitle("Held high risk").querySelector('.job-risk .risk-badge[data-risk="high"]')?.textContent, "High");
  assert.equal(rowByTitle("Plain queued job").querySelector(".job-risk .risk-badge"), null);
  assert.equal(rowByTitle("Legacy PR without merge gate").querySelector(".job-risk .risk-badge"), null);
});

test("Board places auto-merging PRs waiting on CI in Running with Waiting for CI status, not Review", { timeout: 30000 }, async context => {
  const { dom, cleanup } = setupDOM("board");
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom", logLevel: "silent" });
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    cleanup();
  });

  const tickets = [
    {
      ...makeTicket(1, { state: "pr_ready", source: "linear", minutesAgo: 1 }),
      title: "Approved PR waiting on CI",
      pr_number: 201,
      pr_state: "open",
      shipping: '{"since":"2026-11-27T15:00:00Z","head":"abc"}',
    },
    {
      ...makeTicket(2, { state: "pr_ready", source: "linear", minutesAgo: 2 }),
      title: "Held PR needing review",
      pr_number: 202,
      pr_state: "open",
      shipping: '{"hold":"merge gate requires human review"}',
    },
  ];

  const { JobsPage } = await server.ssrLoadModule("/src/jobs-page.jsx");
  await act(async () => root.render(React.createElement(JobsPage, { tickets, runners: [], loaded: true })));

  const runningTitles = [...document.querySelectorAll('[aria-labelledby="board-running"] .board-card-title')].map(el => el.textContent);
  const reviewTitles = [...document.querySelectorAll('[aria-labelledby="board-needs_you"] .board-card-title')].map(el => el.textContent);
  assert.deepEqual(runningTitles, ["Approved PR waiting on CI"]);
  assert.deepEqual(reviewTitles, ["Held PR needing review"]);

  const ciCard = document.querySelector('[aria-labelledby="board-running"] .board-card[data-state="waiting_for_ci"]');
  assert.ok(ciCard, "expected waiting_for_ci card in Running column");
  assert.match(ciCard.querySelector(".pill.pill-info")?.textContent || "", /Waiting for CI/);

  // Switch to List view and confirm the Status column distinguishes Waiting for CI from Needs you.
  const listButton = [...document.querySelectorAll(".range button")].find(button => button.textContent.includes("List"));
  await act(async () => listButton.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
  const rowByTitle = title => [...document.querySelectorAll("table.jobs tbody tr")].find(row => row.querySelector(".job-title")?.textContent === title);
  assert.match(rowByTitle("Approved PR waiting on CI").querySelector(".pill.pill-info")?.textContent || "", /Waiting for CI/);
  assert.match(rowByTitle("Held PR needing review").querySelector(".pill.pill-warning")?.textContent || "", /Needs you/);
});
