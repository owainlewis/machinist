import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

test("automations expose manual and scheduled jobs, progress and editor without disclosure toggles", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { AutomationsPage, AutomationEditor, automationSchedule } = await server.ssrLoadModule("/src/automations.jsx");
  const a = { id:"a", name:"Bug review",repository:"repo",prompt:"Review bugs",cron:"0 9 * * 1-5",timezone:"Europe/London",version:1,paused:true,last_job:"job1" };
  assert.equal(automationSchedule(a),"Weekdays at 09:00 · Europe/London · Paused");
  assert.equal(automationSchedule({...a,cron:""}),"Manual only");
  const html = renderToStaticMarkup(React.createElement(AutomationsPage,{automations:[a],jobs:[{id:"job1",state:"awaiting_merge"}],repositories:["repo"],loaded:true}));
  assert.match(html,/Resume/); assert.match(html,/href="#\/runs\/job1"/); assert.match(html,/Run now/);
  assert.match(html,/repository root/); assert.doesNotMatch(html,/<details|type="checkbox"|role="switch"/);
  const historical = renderToStaticMarkup(React.createElement(AutomationsPage,{automations:[{...a,busy:true}],jobs:[{id:"job1",state:"succeeded"}],repositories:["repo"],loaded:true}));
  assert.match(historical,/An earlier run needs attention/);
  assert.match(historical,/<button[^>]*disabled=""[^>]*>[\s\S]*?Run now/);
  const stopped = renderToStaticMarkup(React.createElement(AutomationsPage,{automations:[{...a,needs_stop_confirmation:true}],jobs:[{id:"job1",state:"cancelled"}],repositories:["repo"],loaded:true}));
  assert.match(stopped,/Confirm stopped and run/);
  const editor = renderToStaticMarkup(React.createElement(AutomationEditor,{initial:a,repositories:["repo"],pending:false}));
  assert.match(editor,/Instructions/); assert.match(editor,/Timezone/); assert.match(editor,/Delete automation/);
});

test("automation waiting messages distinguish running work from pending decisions", async context => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  context.after(() => server.close());
  const { AutomationsPage } = await server.ssrLoadModule("/src/automations.jsx");
  const a = { id: "a", name: "Bug review", repository: "repo", prompt: "Review", cron: "", last_job: "job1" };
  for (const [state, message] of [
    ["queued", "Run queued."],
    ["running", "Run in progress."],
    ["awaiting_merge", "Merge the PR, then check its status."],
    ["awaiting_approval", "Approve the specification to continue."],
    ["blocked", "Resolve the blocker or cancel the task."],
  ]) {
    const html = renderToStaticMarkup(React.createElement(AutomationsPage, { automations: [a], jobs: [{ id: "job1", state }], repositories: ["repo"], loaded: true }));
    assert.ok(html.includes(message), `Missing guidance for ${state}`);
    const dom = new JSDOM(html);
    const run = [...dom.window.document.querySelectorAll("button")].find(button => button.textContent.includes("Run now"));
    assert.equal(run.disabled, true, `${state} allowed another run`);
    dom.window.close();
  }
});

test("Run now retains its admission key after a lost response and links the resulting job", async context => {
  const server = await createServer({ server:{middlewareMode:true,hmr:false},appType:"custom" });
  context.after(() => server.close());
  const { AutomationsPage } = await server.ssrLoadModule("/src/automations.jsx");
  const dom = new JSDOM('<div id="root"></div>',{url:"http://localhost"});
  const previous = {window:global.window,document:global.document,fetch:global.fetch,IS_REACT_ACT_ENVIRONMENT:global.IS_REACT_ACT_ENVIRONMENT};
  Object.assign(global,{window:dom.window,document:dom.window.document,IS_REACT_ACT_ENVIRONMENT:true});
  const requests = []; let refreshes=0;
  global.fetch = async (url,options) => { requests.push({url,...options}); if(requests.length===1) throw new Error("Connection lost; try again"); return {ok:true,json:async()=>({id:"job-1"})}; };
  const root=createRoot(document.getElementById("root"));
  context.after(async()=>{await act(()=>root.unmount());dom.window.close();Object.assign(global,previous);});
  await act(()=>root.render(React.createElement(AutomationsPage,{automations:[{id:"a",name:"Bug review",repository:"repo",prompt:"Review",cron:""}],repositories:["repo"],jobs:[],csrfToken:"csrf",loaded:true,onRefresh:async()=>{refreshes++;}})));
  const clickRun=async()=>{const b=[...document.querySelectorAll("button")].find(b=>b.textContent.includes("Run now"));await act(()=>b.click());};
  await clickRun();assert.match(document.querySelector('[role="alert"]').textContent,/Connection lost/);
  await clickRun();
  assert.equal(requests[0].body,requests[1].body);assert.equal(requests[1].headers["X-Machinist-CSRF"],"csrf");
  assert.equal(refreshes,1);assert.equal(document.querySelector('[role="status"] a').getAttribute("href"),"#/runs/job-1");
});
