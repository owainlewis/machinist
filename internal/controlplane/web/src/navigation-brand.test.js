import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("navigation brand is the plain Machinist wordmark", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(source, /<div className="brand-wordmark">Machinist<\/div>/);
  assert.doesNotMatch(source, /MachinistMark/);
});
