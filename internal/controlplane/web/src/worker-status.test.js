import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("workers show connected and disconnected poll status", async () => {
  const catalog = await readFile(new URL("./catalog.jsx", import.meta.url), "utf8");
  const main = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(catalog, /worker\.connected \? "Connected" : "Disconnected"/);
  assert.match(catalog, /state=\{worker\.connected \? "online" : "offline"\}/);
  assert.match(catalog, /Last seen \{relativeTime\(worker\.last_seen_at\)\}/);
  assert.match(main, /workers\.filter\(\(worker\) => worker\.connected\)/);
  assert.match(main, /worker\$\{online\.length === 1 \? "" : "s"\} online/);
  assert.match(main, /truncate whitespace-nowrap/);
  assert.match(main, /title=\{`\$\{online\.length\} connected · \$\{workers\.length\} registered`\}/);
});
