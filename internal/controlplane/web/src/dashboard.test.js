import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { createServer } from "vite";
import { dashboardState } from "./dashboard-state.js";
import { filterJobs, jobCounts } from "./runs-board.js";

const now = new Date(2026, 9, 15, 12);
const job = (id, state, age = 0) => ({id,state,repository:"owner/repo",task:{title:`Task ${id}`},created_at:new Date(2026,9,15-age,10).toISOString(),updated_at:new Date(2026,9,15-age,11).toISOString(),runs:[]});
const view = (jobs, extra={}) => dashboardState({jobs,days:"7",now,loaded:true,error:"",...extra});

test("current workload is independent of the activity window, and cleared attention stays cleared",()=>{
  const jobs=[job("old","running",40),job("pr","awaiting_merge",20),job("held","blocked"),{...job("cleared","failed"),inbox_cleared:true},job("cancelled","cancelled")];
  const state=view(jobs);
  assert.deepEqual(state.active.map(j=>j.id),["old"]);
  assert.deepEqual(state.ready.map(j=>j.id),["pr"]);
  assert.deepEqual(state.attention.map(j=>j.id),["held"]);
  assert.deepEqual(filterJobs(jobs,"attention").map(j=>j.id),["held"]);
  assert.deepEqual(filterJobs(jobs,"pr_ready").map(j=>j.id),["pr"]);
  const counts=jobCounts(jobs);assert.equal(counts.attention,1);assert.equal(counts.pr_ready,1);
  assert.equal(state.metrics.totalTasks,3);assert.equal(state.counts.cancelled,1);
});

test("activity counts local calendar days, excludes future jobs, and separates unfinished and cancelled outcomes",()=>{
  const jobs=[job("a","succeeded"),job("b","timed_out",1),job("c","cancelled",2),job("d","awaiting_merge",3),job("future","queued",-1)];
  const state=view(jobs);
  assert.equal(state.dates.length,7);assert.equal(state.dates.reduce((n,d)=>n+d.count,0),4);
  assert.equal(state.dates.at(-1).count,1);
  assert.deepEqual(state.counts,{succeeded:1,failed:1,cancelled:1,unfinished:1});
  assert.equal(state.usage.total,undefined);assert.equal(state.timedMillis,null);
  assert.equal(state.recent.length,4);
});

test("usage uses exact reported token counts and exposes partial timing coverage",()=>{
  const item=job("measured","succeeded");item.runs=[{id:"one",completed_at:now.toISOString(),duration_millis:500,token_usage:"9007199254740993"},{id:"two",completed_at:now.toISOString()}];
  const state=view([item],{workers:[{connected:true},{connected:false}],automations:[{cron:"* * * * *",paused:false},{cron:"* * * * *",paused:true},{cron:""}]});
  assert.equal(state.usage.total,"9007199254740993");assert.equal(state.usage.unavailable,1);
  assert.equal(state.timedRuns,1);assert.equal(state.timedMillis,500);assert.equal(state.onlineWorkers,1);assert.equal(state.scheduled,1);
});

test("failed refreshes never present stale healthy metrics",()=>{
  assert.deepEqual(view([job("x","running")],{error:"offline"}),{kind:"error",message:"offline"});
  assert.deepEqual(view([],{loaded:false}),{kind:"loading"});
});

test("dashboard renders useful job links, missing usage and honest empty activity",async context=>{
  const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
  const {Dashboard}=await server.ssrLoadModule("/src/analytics.jsx");
  const html=renderToStaticMarkup(React.createElement(Dashboard,{jobs:[job("ready","awaiting_merge")],loaded:true,now}));
  assert.match(html,/<h1[^>]*>Dashboard/);assert.match(html,/href="#\/runs\?filter=pr_ready"/);assert.match(html,/href="#\/runs\/ready"/);
  assert.match(html,/href="#\/runs">View runs/);assert.doesNotMatch(html,/#\/inbox|Open inbox/);
  assert.match(html,/Not reported/);assert.doesNotMatch(html,/100%|\$0|<details|role="switch"/);
  const empty=renderToStaticMarkup(React.createElement(Dashboard,{jobs:[],loaded:true,now}));
  assert.match(empty,/Create your first run/);assert.match(empty,/0 jobs at peak/);assert.doesNotMatch(empty,/1 job at peak/);
  const failure=renderToStaticMarkup(React.createElement(Dashboard,{jobs:[job("ready","awaiting_merge")],loaded:true,error:"offline",now}));
  assert.match(failure,/Dashboard unavailable/);assert.doesNotMatch(failure,/PRs ready/);
});

test("the window control updates activity while retaining live work and New run starts the composer",async context=>{
  const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
  const {Dashboard}=await server.ssrLoadModule("/src/analytics.jsx");
  const dom=new JSDOM('<div id="root"></div>',{url:"http://localhost"});
  const previous={window:global.window,document:global.document,IS_REACT_ACT_ENVIRONMENT:global.IS_REACT_ACT_ENVIRONMENT};
  Object.assign(global,{window:dom.window,document:dom.window.document,IS_REACT_ACT_ENVIRONMENT:true});
  const root=createRoot(document.getElementById("root"));context.after(async()=>{await act(()=>root.unmount());dom.window.close();Object.assign(global,previous);});
  let starts=0;
  await act(()=>root.render(React.createElement(Dashboard,{jobs:[job("older","running",20)],loaded:true,now,onNewRun:()=>starts++})));
  assert.equal(document.querySelector('.dashboard-activity-total strong').textContent,"0");
  assert.equal(document.querySelector('.dashboard-stat strong').textContent,"1");
  await act(()=>{const select=document.querySelector('select');select.value="30";select.dispatchEvent(new dom.window.Event("change",{bubbles:true}));});
  assert.equal(document.querySelector('.dashboard-activity-total strong').textContent,"1");
  await act(()=>[...document.querySelectorAll("button")].find(b=>b.textContent==="New run").click());assert.equal(starts,1);
});

test("legacy servers without saved automation data never claim zero configured schedules", async context=>{
  const state=view([]);assert.equal(state.scheduled,null);assert.equal(state.automationCount,null);
  const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
  const {Dashboard}=await server.ssrLoadModule("/src/analytics.jsx");
  const html=renderToStaticMarkup(React.createElement(Dashboard,{jobs:[],loaded:true,now}));
  assert.match(html,/View configured triggers/);assert.doesNotMatch(html,/0 active schedule|0 saved automation/);
  const known=renderToStaticMarkup(React.createElement(Dashboard,{jobs:[],automations:[],loaded:true,now}));
  assert.match(known,/0 active schedules/);
});
