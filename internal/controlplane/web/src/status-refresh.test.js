import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("every successful status refresh clears a prior request error", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  const refresh = source.match(/async function refresh\(\) \{([\s\S]*?)\n  \}\n\n  useEffect/);

  assert.ok(refresh, "refresh function should be present");
  assert.match(refresh[1], /setRepository\([\s\S]*setStatusError\(""\);/);
  assert.match(source, /async function submit\([\s\S]*await refresh\(\);/);
});
