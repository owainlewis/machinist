import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("workers use live status copy while agents retain configuration copy", async () => {
  const catalog = await readFile(new URL("./catalog.jsx", import.meta.url), "utf8");
  const settings = await readFile(new URL("./settings.jsx", import.meta.url), "utf8");

  assert.match(catalog, /Checking live worker status\./);
  assert.match(catalog, /Start a worker to register this machine with the control plane\./);
  assert.match(settings, /Loading the latest configuration\./);
  assert.match(settings, /Configuration added on the control plane will appear here\./);
});
