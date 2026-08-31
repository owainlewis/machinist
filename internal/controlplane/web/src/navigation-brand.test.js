import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("navigation brand uses the interlocking two-piece mark and wordmark", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(source, /<MachinistMark \/>/);
  assert.match(source, /className="brand-wordmark">machinist<\/span>/);
  assert.match(source, /function MachinistMark\(\)/);
  assert.match(source, /className="machinist-mark-piece-a"/);
  assert.match(source, /className="machinist-mark-piece-b"/);
  assert.doesNotMatch(source, /<circle/);
});
