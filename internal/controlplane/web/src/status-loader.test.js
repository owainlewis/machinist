import assert from "node:assert/strict";
import test from "node:test";
import { createStatusLoader } from "./status-loader.js";

test("an older success cannot replace a newer request failure", async () => {
  const first = deferred();
  const second = deferred();
  const requests = [first.promise, second.promise];
  const applied = [];
  const loader = createStatusLoader({ request: () => requests.shift(), apply: (result) => applied.push(result) });

  const olderRefresh = loader.refresh();
  const newerRefresh = loader.refresh();
  second.reject(new Error("newer request failed"));
  await newerRefresh;
  first.resolve({ tickets: [] });
  await olderRefresh;

  assert.deepEqual(applied, [{ kind: "error", message: "newer request failed" }]);
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}
