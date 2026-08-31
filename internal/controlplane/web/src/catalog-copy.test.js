import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("workers use live status copy while commands retain configuration copy", async () => {
  const source = await readFile(new URL("./catalog.jsx", import.meta.url), "utf8");

  assert.match(source, /Checking live worker status\./);
  assert.match(source, /Start a worker to register this machine with the control plane\./);
  assert.match(source, /Loading the latest configuration\./);
  assert.match(source, /Configuration added on the control plane will appear here\./);
});
