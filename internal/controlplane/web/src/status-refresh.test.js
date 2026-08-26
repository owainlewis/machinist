import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { refreshAfterSubmit } from "./status-refresh.js";

test("every successful status refresh clears a prior request error", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  const refresh = source.match(/async function refresh\(\) \{([\s\S]*?)\n  \}\n\n  useEffect/);

  assert.ok(refresh, "refresh function should be present");
  assert.match(refresh[1], /setRepository\([\s\S]*setStatusError\(""\);/);
  assert.match(source, /async function submit\([\s\S]*await refreshAfterSubmit\(refresh, setStatusError\);/);
});

test("a failed submit-triggered refresh reaches analytics and the runs form", async () => {
  const statusErrors = [];
  let submitError = "";

  try {
    await refreshAfterSubmit(
      async () => { throw new Error("Status request failed (503)"); },
      (message) => statusErrors.push(message),
    );
  } catch (requestError) {
    submitError = requestError.message;
  }

  assert.deepEqual(statusErrors, ["Status request failed (503)"]);
  assert.equal(submitError, "Status request failed (503)");
});
