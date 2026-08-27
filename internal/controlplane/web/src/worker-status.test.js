import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("workers show connected and disconnected poll status", async () => {
  const catalog = await readFile(new URL("./catalog.jsx", import.meta.url), "utf8");
  const main = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(catalog, /worker\.connected \? "Connected" : "Disconnected"/);
  assert.match(catalog, /worker\.connected \? "[^"]*text-success"/);
  assert.match(main, /status\.workers\.filter\(\(worker\) => worker\.connected\)\.length/);
  assert.match(main, /connected · \{status\.workers\.length\} registered/);
});
