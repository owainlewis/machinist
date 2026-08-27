import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("navigation brand enlarges the mark without changing its layout box", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(source, /className="grid size-7 place-items-center[^\"]*"><Workflow className="size-\[18px\]" \/><\/span>/);
});
