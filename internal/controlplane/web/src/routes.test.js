import assert from "node:assert/strict";
import test from "node:test";
import { routeFromHash } from "./routes.js";

test("the four pages are addressable", () => {
  for (const view of ["assign", "work", "automations", "runners"]) assert.deepEqual(routeFromHash(`#/${view}`), { view, ticketID: "" });
});

test("assign is the default and unknown routes return to it", () => {
  for (const hash of ["", "#/", "#/unknown", "#/runs"]) assert.deepEqual(routeFromHash(hash), { view: "assign", ticketID: "" });
});

test("a ticket has its own route", () => {
  assert.deepEqual(routeFromHash("#/work/t_123"), { view: "ticket", ticketID: "t_123" });
  assert.deepEqual(routeFromHash("#/work/t%2F1"), { view: "ticket", ticketID: "t/1" });
});

test("incomplete or malformed ticket routes open Work", () => {
  assert.deepEqual(routeFromHash("#/work/"), { view: "assign", ticketID: "" });
  assert.deepEqual(routeFromHash("#/work/%E0%A4%A"), { view: "work", ticketID: "" });
});
