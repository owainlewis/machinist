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
      const active = matching.filter(t => t.state === "working");
      const done = matching.filter(t => t.state !== "working");
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
