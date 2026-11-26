import assert from "node:assert/strict";
import test from "node:test";
import { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

async function mountSignIn(context, { url, authStatus, fetchImpl }) {
  const dom = new JSDOM('<div id="root"></div>', { url });
  const previous = new Map();
  for (const name of ["window", "document", "navigator", "localStorage", "Event", "MouseEvent", "HTMLElement", "getComputedStyle", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    previous.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    if (name in dom.window) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  }
  globalThis.fetch = fetchImpl;
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  let root;
  context.after(async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    await act(async () => root?.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  globalThis.IS_REACT_ACT_ENVIRONMENT = false;
  const React = await server.ssrLoadModule("react");
  const { createRoot } = await server.ssrLoadModule("react-dom/client");
  const { SignIn } = await server.ssrLoadModule("/src/sign-in.jsx");
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  root = createRoot(document.getElementById("root"));
  await act(async () => {
    root.render(React.createElement(SignIn, { authStatus, onSignedIn: () => {} }));
  });
}

test("GitHub OAuth mode hides token input and prepares fragment invites", { timeout: 30000 }, async context => {
  const preparedTokens = [];
  await mountSignIn(context, {
    url: "http://localhost/#/invite#minv_secret123",
    authStatus: { methods: ["github"], token_signin_enabled: false },
    fetchImpl: async (url, options = {}) => {
      if (url === "/api/v1/invites/prepare" && options.method === "POST") {
        preparedTokens.push(JSON.parse(options.body).token);
        return { ok: true, status: 200, json: async () => ({ valid: true, role: "member", target_hint: "github:alice", start_url: "/auth/github/start" }) };
      }
      throw new Error(`unexpected ${url}`);
    },
  });

  // Fragment secret was cleared from URL and sent via POST.
  assert.deepEqual(preparedTokens, ["minv_secret123"]);
  assert.equal(window.location.hash, "#/sign-in");
  assert.equal(document.querySelector("#token"), null, "token input hidden when token_signin_enabled is false");
  const oauthLink = document.querySelector(".sign-in-oauth-btn");
  assert.ok(oauthLink);
  assert.equal(oauthLink.getAttribute("href"), "/auth/github/start");
  assert.match(document.querySelector('[role="status"]').textContent, /invited as member/);
});

test("SignIn shows friendly error banners for OAuth denial reasons", { timeout: 30000 }, async context => {
  await mountSignIn(context, {
    url: "http://localhost/#/sign-in?error=not_invited",
    authStatus: { methods: ["github"], token_signin_enabled: false },
    fetchImpl: async () => { throw new Error("unexpected fetch"); },
  });
  assert.match(document.querySelector('[role="alert"]').textContent, /not invited/i);
});
