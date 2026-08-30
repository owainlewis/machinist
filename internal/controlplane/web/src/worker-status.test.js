import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("workers show connected and disconnected poll status", async () => {
  const catalog = await readFile(new URL("./catalog.jsx", import.meta.url), "utf8");
  const main = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(catalog, /worker\.connected \? "Connected" : "Disconnected"/);
  assert.match(catalog, /worker\.connected \? "[^"]*text-success"/);
  assert.match(catalog, /Last seen \{relativeTime\(worker\.last_seen_at\)\}/);
  assert.match(main, /status\.workers\.filter\(\(worker\) => worker\.connected\)\.length/);
  assert.match(main, /worker\$\{connectedWorkers === 1 \? "" : "s"\} online/);
  assert.match(main, /truncate whitespace-nowrap/);
  assert.match(main, /title=\{`\$\{connectedWorkers\} connected · \$\{status\.workers\.length\} registered`\}/);
});

test("workers can remove disconnected registrations", async () => {
  const catalog = await readFile(new URL("./catalog.jsx", import.meta.url), "utf8");
  const main = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(catalog, /disabled=\{worker\.connected \|\| deleting === worker\.instance_id\}/);
  assert.match(catalog, /Connected workers cannot be removed/);
  assert.match(catalog, /onClick=\{\(\) => onDelete\(worker\)\}/);
  assert.match(main, /window\.confirm\(`Remove disconnected worker \$\{worker\.name\} \(\$\{worker\.instance_id\}\)\?`\)/);
  assert.match(main, /fetch\(`\/api\/v1\/workers\/\$\{encodeURIComponent\(worker\.instance_id\)\}`/);
  assert.match(main, /await statusLoader\.current\.refresh\(\)/);
  assert.match(main, /workerActionError/);
});
