import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("navigation brand uses the custom scribed-line mark and wordmark", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(source, /<MachinistMark \/>/);
  assert.match(source, /className="brand-wordmark">machinist<\/span>/);
  assert.match(source, /function MachinistMark\(\)/);
  assert.match(source, /<circle cx="42" cy="21" r="2" \/>/);
});
