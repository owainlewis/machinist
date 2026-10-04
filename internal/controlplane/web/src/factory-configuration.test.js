import assert from 'node:assert/strict';
import test from 'node:test';
import React, {act} from 'react';
import {JSDOM} from 'jsdom';
import {createServer} from 'vite';
import {copyConfiguration,configurationErrors,commandLines} from './factory-configuration-state.js';
const fixture=()=>({revision:0,scope:'global_future_tasks',foreman:'planner',default_pipeline:'standard',agents:{planner:{name:'Planning agent',description:'Plans work',prompt:'Plan carefully',runtime:'claude',model:'',timeout:'30m'}},pipelines:{standard:{name:'Standard',steps:[{id:'design',name:'Design',type:'agent',stage:'Design',agent:'planner'},{id:'gate',name:'Approve design',type:'approval',stage:'Design',subject:'design'},{id:'check',name:'Checks',type:'script',stage:'Review',command:['npm','test']}]}}});
test('configuration preserves snapshots and literal script arguments with optional timeout',()=>{const config=fixture();assert.deepEqual(configurationErrors(config),[]);const draft=copyConfiguration(config);draft.agents.planner.prompt='Changed';assert.equal(config.agents.planner.prompt,'Plan carefully');assert.deepEqual(commandLines('printf\na b\n$HOME\n'),['printf','a b','$HOME','']);draft.pipelines.standard.steps[2].command=[''];assert.match(configurationErrors(draft).join(' '),/executable/);});
test('settings save a full revision snapshot and preserve edits after a stale response',async context=>{
 const dom=new JSDOM('<div id="root"></div>',{url:'http://localhost',pretendToBeVisual:true});const prior=new Map();
 for(const name of ['window','document','navigator','HTMLElement','HTMLInputElement','HTMLTextAreaElement','Node','MutationObserver','CustomEvent','Event','MouseEvent','getComputedStyle','requestAnimationFrame','cancelAnimationFrame','fetch','IS_REACT_ACT_ENVIRONMENT']){prior.set(name,Object.getOwnPropertyDescriptor(globalThis,name));Object.defineProperty(globalThis,name,{configurable:true,writable:true,value:dom.window[name]});}
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 let persisted=fixture(),stale=false,saved=0;const requests=[];
 globalThis.fetch=async(url,options={})=>{requests.push({url,options});if(options.method==='PUT'){if(stale)return {ok:false,status:409,json:async()=>({error:'Configuration changed. Reload its latest version.'})};persisted={...JSON.parse(options.body),revision:persisted.revision+1};}return {ok:true,json:async()=>copyConfiguration(persisted)};};
 const server=await createServer({server:{middlewareMode:true,hmr:false,ws:false},appType:'custom'});const {PipelineSettings}=await server.ssrLoadModule('/src/factory-configuration.jsx');const {createRoot}=await import('react-dom/client');const root=createRoot(document.getElementById('root'));
 context.after(async()=>{await act(async()=>root.unmount());await server.close();dom.window.close();for(const[name,value]of prior){if(value)Object.defineProperty(globalThis,name,value);else delete globalThis[name];}});
 await act(async()=>root.render(React.createElement(PipelineSettings,{csrfToken:'csrf',onSaved:()=>{saved++;}})));
 const button=text=>[...document.querySelectorAll('button')].find(el=>el.textContent.trim()===text);
 const click=async el=>{assert.ok(el);await act(async()=>el.click());};
 const input=async(id,value)=>{const el=document.getElementById(id);assert.ok(el,id);await act(async()=>{const prototype=el.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;Object.getOwnPropertyDescriptor(prototype,'value').set.call(el,value);el.dispatchEvent(new Event('input',{bubbles:true}));});};
 const design=[...document.querySelectorAll('.configuration-pipeline button')][0];await click(design);await click(button('Edit profile'));
 await input('configuration-agent-prompt','Plan with evidence');await act(async()=>document.querySelector('form').dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));
 assert.equal(saved,1);assert.equal(persisted.revision,1);assert.equal(persisted.agents.planner.prompt,'Plan with evidence');assert.deepEqual(persisted.pipelines,fixture().pipelines);assert.equal(requests[0].options.headers['X-Machinist-CSRF'],'csrf');
 await input('configuration-agent-prompt','Keep my draft');stale=true;await act(async()=>document.querySelector('form').dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));
 assert.match(document.body.textContent,/Your edits are still here/);assert.equal(document.getElementById('configuration-agent-prompt').value,'Keep my draft');assert.equal(saved,1);
 await click(button('Discard changes'));assert.equal(document.getElementById('configuration-agent-prompt').value,'Plan with evidence');
});
