import test from "node:test";
import assert from "node:assert/strict";
import { reduceActivity, pollRunActivity, agentLabel, agentOutcome, agentInstanceLabels } from "./native-activity.js";
const activity = (id, status, time) => ({ type: "agent.activity", time, message: JSON.stringify({ native_id: id, name: "Reader", status }) });
test("parallel identities, primary log and honest interruption", () => {
 const events = [activity("a", "running", "start"), activity("b", "running", "later"), activity("a", "failed", "end"), { type: "agent.message", message: JSON.stringify({ primary: false, text: "CHILD" }) }, { type: "agent.message", message: JSON.stringify({ primary: true, text: "PARENT" }) }];
 const result = reduceActivity(events, "running");
 assert.equal(result.children.length, 2);
 assert.equal(result.children[0].status, "failed");
 assert.equal(result.children[0].started_at, "start");
 assert.deepEqual(result.logs, ["PARENT"]);
 assert.equal(reduceActivity(events, "interrupted").children[1].status, "interrupted");
 assert.equal(reduceActivity(events, "running").children[1].status, "running");
});

test("overview is compact and full reports have a separate reader", async context => {
  const {createServer}=await import("vite"); const React=await import("react"); const {renderToStaticMarkup}=await import("react-dom/server");
  const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
  const {ExecutionGraph,AgentReports,PrimaryLog}=await server.ssrLoadModule("/src/native-activity.jsx");
  const summary="Accepted the change.\n\nFULL_REPORT_ONLY "+"Evidence and details. ".repeat(30);
  const children=[{native_id:"review",role:"reviewer",name:"Inspect the patch",status:"completed",summary}];
  const overview=renderToStaticMarkup(React.createElement(ExecutionGraph,{activities:children,runState:"succeeded"}));
  assert.match(overview,/Reviewer/);assert.match(overview,/Complete/);assert.match(overview,/Accepted the change/);assert.match(overview,/Read Reviewer report/);
  assert.doesNotMatch(overview,/FULL_REPORT_ONLY|Inspect the patch|<details|<summary|milliseconds/);
  const report=renderToStaticMarkup(React.createElement(AgentReports,{activities:children,selectedID:"review"}));
  assert.match(report,/FULL_REPORT_ONLY/);assert.match(report,/Inspect the patch/);assert.doesNotMatch(report,/<details|<summary/);
  const log=renderToStaticMarkup(React.createElement(PrimaryLog,{logs:[JSON.stringify({outcome:"no_change",summary:"Finished"})]}));assert.match(log,/Finished/);
});

const rawEvent = object => ({type:"process.output",stream:"stdout",encoding:"base64",data:Buffer.from(JSON.stringify(object)+"\n").toString("base64")});
test("historical Claude graph excludes shell tasks and restores delegation names",()=>{
 const events=[rawEvent({type:"system",subtype:"task_started",task_type:"local_agent",task_id:"reader",description:"Review code"}),rawEvent({type:"system",subtype:"task_started",task_type:"local_bash",task_id:"shell",description:"npm test"}),activity("reader","running","start"),activity("shell","completed","later"),{...activity("reader","completed","end"),message:JSON.stringify({native_id:"reader",name:"Running git diff",status:"completed",summary:"Accepted"})}];
 const result=reduceActivity(events,"succeeded");assert.equal(result.children.length,1);assert.equal(result.children[0].name,"Review code");assert.equal(result.children[0].summary,"Accepted");
});
test("historical Codex excludes the primary and interactions preserve child state",()=>{
 const events=[rawEvent({id:4,result:{thread:{id:"root"}}}),rawEvent({method:"item/started",params:{item:{type:"subAgentActivity",kind:"started",agentThreadId:"child",agentPath:"/root/reviewer"}}}),activity("child","running","start"),activity("child","unknown","interacted"),activity("root","unknown","interacted")];
 const result=reduceActivity(events,"running");assert.equal(result.children.length,1);assert.equal(result.children[0].status,"running");assert.equal(result.children[0].name,"/root/reviewer");
});

test("a script stage shows what it printed, and an agent stage does not",()=>{
 const output=(stream,text)=>({type:"process.output",stream,encoding:"base64",data:Buffer.from(text).toString("base64")});
 const stage=(name,kind)=>({type:"stage.started",message:`name=${name} kind=${kind}`});
 const events=[stage("build","agent"),output("stdout","AGENT JSON\n"),stage("test","script"),output("stdout","ok: 1 check\n"),output("stderr","FAIL: broken line\n"),stage("check","agent"),output("stdout","MORE JSON\n")];
 assert.deepEqual(reduceActivity(events,"failed").scripts,[{name:"test",output:"ok: 1 check\nFAIL: broken line\n"}]);
 assert.deepEqual(reduceActivity([output("stdout","no stages\n")],"succeeded").scripts,[]);
});

test("cancelled activity continues polling until cleanup acknowledgment",()=>{
 assert.equal(pollRunActivity({state:"cancelled",phase:"stopping"}),true);
 assert.equal(pollRunActivity({state:"cancelled",completed_at:"2026-10-21T10:00:00Z"}),false);
 assert.equal(pollRunActivity({state:"cancelled"}),false);
});

test("role comes from selected definition, not description or task name",()=>{
 const events=[rawEvent({type:"assistant",message:{content:[{type:"tool_use",id:"tool",name:"Agent",input:{subagent_type:"reviewer"}}]}}),rawEvent({type:"system",subtype:"task_started",task_type:"local_agent",task_id:"a",tool_use_id:"tool",description:"Fix everything"}),activity("a","completed","end")];
 const child=reduceActivity(events,"succeeded").children[0];assert.equal(child.role,"reviewer");assert.equal(agentLabel(child),"Reviewer");
 const codex=[rawEvent({method:"item/started",params:{item:{type:"subAgentActivity",kind:"started",agentThreadId:"a",agentPath:"/root/arbitrary"}}}),rawEvent({id:101,result:{thread:{id:"a",agentRole:"triage"}}}),activity("a","completed","end"),rawEvent({method:"item/completed",params:{threadId:"a",item:{type:"agentMessage",text:"Expanded the ticket."}}})];
 const c=reduceActivity(codex,"succeeded").children[0];assert.equal(c.role,"triage");assert.equal(c.summary,"Expanded the ticket.");
 assert.equal(agentLabel({name:"Run review commands"}),"Subagent");
 assert.equal(agentOutcome({status:"running"}),"Working. No outcome yet.");
 assert.ok(agentOutcome({summary:"word ".repeat(200)}).length<=150);
});

test("fresh review attempts are individually selectable without implying pipeline stages",()=>{
 const names=agentInstanceLabels([{native_id:"a",role:"reviewer"},{native_id:"b",role:"reviewer"},{native_id:"c",role:"triage"}]);assert.deepEqual([...names.values()],["Reviewer 1","Reviewer 2","Triage"]);
});

test("selecting a role opens its full report without a duplicate overview feed", async context=>{
 const {JSDOM}=await import("jsdom");const React=await import("react");const {createRoot}=await import("react-dom/client");const {createServer}=await import("vite");
 const dom=new JSDOM('<div id="test-root"></div>',{url:"http://localhost"});
 let root;
 const previous={window:globalThis.window,document:globalThis.document,IS_REACT_ACT_ENVIRONMENT:globalThis.IS_REACT_ACT_ENVIRONMENT};
 globalThis.window=dom.window;globalThis.document=dom.window.document;globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 context.after(async()=>{if(root) await React.act(async()=>root.unmount()); for(const [key,value] of Object.entries(previous)) {if(value===undefined)delete globalThis[key];else globalThis[key]=value;}dom.window.close();});
 const events=[{type:"agent.activity",sequence:1,message:JSON.stringify({native_id:"first",role:"reviewer",name:"First review",status:"completed",summary:"Changes requested.\n\nFULL_FIRST_REPORT"})},{type:"agent.activity",sequence:2,message:JSON.stringify({native_id:"second",role:"reviewer",name:"Fresh review",status:"completed",summary:"Accepted.\n\nFULL_SECOND_REPORT"})}];
 context.mock.method(globalThis,"fetch",async()=>({ok:true,json:async()=>({cursor:2,events})}));
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});context.after(()=>server.close());
 const {RunActivity}=await server.ssrLoadModule("/src/task-detail.jsx");root=createRoot(document.getElementById("test-root"));
 const run={id:"run",status:"succeeded"};
 await React.act(async()=>{root.render(React.createElement(RunActivity,{run}));});
 let visible=document.querySelector('.run-activity');assert.match(visible.textContent,/Reviewer 1/);assert.doesNotMatch(visible.textContent,/FULL_SECOND_REPORT/);
 await React.act(async()=>document.querySelector('[aria-label="Read Reviewer 2 report"]').click());
 visible=document.querySelector('[aria-label="Agent reports"]');assert.match(visible.textContent,/FULL_SECOND_REPORT/);assert.doesNotMatch(visible.textContent,/FULL_FIRST_REPORT/);
 await React.act(async()=>document.querySelector('[aria-label="Choose an agent report"] button').click());
 assert.match(visible.textContent,/FULL_FIRST_REPORT/);assert.doesNotMatch(visible.textContent,/FULL_SECOND_REPORT/);
});

test("full Claude child reports survive a later short notification",()=>{
 const events=[rawEvent({type:"system",subtype:"task_started",task_type:"local_agent",task_id:"a",tool_use_id:"tool",description:"Review"}),activity("a","completed","end"),{type:"agent.message",message:JSON.stringify({native_id:"a",primary:false,text:"Full findings. Details that must survive."})},rawEvent({type:"system",subtype:"task_notification",task_id:"a",summary:"Accepted"})];
 assert.equal(reduceActivity(events,"succeeded").children[0].summary,"Full findings. Details that must survive.");
 events[2].message=JSON.stringify({native_id:"tool",primary:false,text:"Historical full findings."});
 assert.equal(reduceActivity(events,"succeeded").children[0].summary,"Historical full findings.");
});

test("raw full reports retain their tail beyond the normalized message limit",()=>{
 const full="Evidence ".repeat(1400)+"DISTINCT_FINAL_FINDING";
 const events=[rawEvent({type:"system",subtype:"task_started",task_type:"local_agent",task_id:"a",tool_use_id:"tool"}),activity("a","completed","end"),rawEvent({type:"assistant",agent_id:"a",parent_tool_use_id:"tool",message:{content:[{type:"text",text:full}]}}),{type:"agent.message",message:JSON.stringify({native_id:"a",primary:false,text:full.slice(0,8192)+"[truncated]"})},rawEvent({type:"system",subtype:"task_notification",task_id:"a",summary:"Accepted"})];
 assert.equal(reduceActivity(events,"succeeded").children[0].summary,full);
});

test("raw Codex reports retain their tail beyond the normalized limit",()=>{
 const full="Evidence ".repeat(1400)+"CODEX_DISTINCT_FINAL_FINDING";
 const events=[rawEvent({method:"item/started",params:{item:{type:"subAgentActivity",kind:"started",agentThreadId:"a",agentPath:"/root/reviewer"}}}),activity("a","completed","end"),rawEvent({method:"item/completed",params:{threadId:"a",item:{type:"agentMessage",text:full}}}),{type:"agent.message",message:JSON.stringify({native_id:"a",primary:false,text:full.slice(0,8192)+"[truncated]"})}];
 assert.equal(reduceActivity(events,"succeeded").children[0].summary,full);
});
