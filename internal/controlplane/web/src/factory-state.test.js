import { taskDetailKey, mergeTaskDetail } from "./factory-state.js";
import test from "node:test";
import assert from "node:assert/strict";
import {
  groupTasks,
  displayEvents,
  isBusy,
  factoryView,
  createConversationLoader,
} from "./factory-state.js";
test("completion does not imply delivery", () => {
  const g = groupTasks([
    { id: "a", stage: "Review", status: "completed" },
    { id: "b", stage: "Done" },
  ]);
  assert.equal(g.Review[0].id, "a");
  assert.equal(g.Done[0].id, "b");
});
test("chunks combine within a turn", () => {
  const e = displayEvents([
    { kind: "message_delta", turn_id: "a", text: "Hi " },
    { kind: "message_delta", turn_id: "a", text: "you" },
    { kind: "message_delta", turn_id: "b", text: "Next" },
  ]);
  assert.equal(e.length, 2);
  assert.equal(e[0].text, "Hi you");
});
test("permission waiting still allows cancellation", () =>
  assert.equal(isBusy({ status: "awaiting_permission" }), true));

test("activity updates avoid empty and repeated entries while retaining errors", () => {
  const events = displayEvents([
    { kind: "activity" },
    { kind: "activity", title: "Read files", status: "running" },
    { kind: "activity", title: "Read files", status: "running" },
    { kind: "activity", title: "Read files", status: "completed" },
    { kind: "error", text: "Read failed" },
    { kind: "error", text: "Read failed" },
  ]);
  assert.equal(events.length, 4);
  assert.equal(events[1].status, "completed");
  assert.equal(events[2].text, "Read failed");
  assert.equal(events[3].kind, "error");
});

test("legacy links stay inside factory navigation", () => {
  assert.equal(factoryView("#/runs"), "history");
  assert.equal(factoryView("#/runs/old"), "history");
  assert.equal(factoryView("#/workers"), "settings");
  assert.equal(factoryView("#/factory/board"), "board");
  assert.equal(factoryView("#/factory/add"), "add");
  assert.equal(factoryView("#/factory"), "chat");
});

test("conversation refresh fetches only new events and updates permission metadata", async () => {
  const calls = [];
  const loader = createConversationLoader(async (path) => {
    calls.push(path);
    return calls.length === 1
      ? {
          session: { id: "one", status: "running" },
          events: [{ id: 1, text: "first" }],
          permissions: [],
        }
      : {
          session: { id: "one", status: "awaiting_permission" },
          events: [
            { id: 1, text: "duplicate" },
            { id: 2, text: "second" },
          ],
          permissions: [{ id: "permission" }],
        };
  });
  await loader.load("one");
  const next = await loader.load("one");
  assert.deepEqual(calls, ["/sessions/one?cursor=0", "/sessions/one?cursor=1"]);
  assert.deepEqual(
    next.events.map((e) => e.text),
    ["first", "second"],
  );
  assert.equal(next.permissions[0].id, "permission");
});

test("conversation cursors are session scoped and reset on project change", async () => {
  const calls = [];
  const loader = createConversationLoader(async (path) => {
    calls.push(path);
    return { events: [{ id: 4 }] };
  });
  await loader.load("one");
  await loader.load("two");
  await loader.load("one");
  loader.reset();
  await loader.load("one");
  assert.deepEqual(calls, [
    "/sessions/one?cursor=0",
    "/sessions/two?cursor=0",
    "/sessions/one?cursor=4",
    "/sessions/one?cursor=0",
  ]);
});

test("concurrent conversation refreshes serialize cursors and page new events", async () => {
  const calls = [];
  const loader = createConversationLoader(async (path) => {
    calls.push(path);
    return {
      events: path.endsWith("cursor=0")
        ? Array.from({ length: 100 }, (_, i) => ({ id: i + 1 }))
        : path.endsWith("cursor=100")
          ? [{ id: 101 }]
          : [],
    };
  });
  const [first, second] = await Promise.all([
    loader.load("one"),
    loader.load("one"),
  ]);
  assert.equal(first.events.length, 101);
  assert.equal(second.events.length, 101);
  assert.deepEqual(calls, [
    "/sessions/one?cursor=0",
    "/sessions/one?cursor=100",
    "/sessions/one?cursor=101",
  ]);
});

test("task details ignore new task arrays and activity but track revision/check decisions", () => {
  const task = {
    id: "one",
    version: 3,
    status: "active",
    revision: "before",
    check_state: "passed-before",
  };
  const key = taskDetailKey(task);
  assert.equal(
    taskDetailKey({
      ...task,
      check_state: task.check_state,
      activity: "Read another file",
    }),
    key,
  );
  assert.notEqual(taskDetailKey({ ...task, status: "awaiting_approval" }), key);
  assert.notEqual(taskDetailKey({ ...task, revision: "after" }), key);
  assert.notEqual(taskDetailKey({ ...task, check_state: "failed-after" }), key);
});

test("late task detail retains already refreshed matching worker output", () => {
  const current = {
    task: { id: "t" },
    sessions: [
      {
        id: "worker",
        status: "awaiting_permission",
        events: [{ id: 2, text: "fresh" }],
        permissions: [{ id: "p" }],
      },
    ],
  };
  const next = {
    task: { id: "t", revision: "new" },
    diff: "updated diff",
    sessions: [{ id: "worker", status: "running" }, { id: "new-worker" }],
  };
  const merged = mergeTaskDetail(next, current);
  assert.equal(merged.diff, "updated diff");
  assert.equal(merged.sessions[0].events[0].text, "fresh");
  assert.equal(merged.sessions[0].permissions[0].id, "p");
  assert.equal(merged.sessions[1].id, "new-worker");
  assert.deepEqual(
    mergeTaskDetail({ ...next, task: { id: "different" } }, current).sessions,
    next.sessions,
  );
});
