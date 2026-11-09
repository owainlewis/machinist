import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("every page has one heading in the shared page frame", async () => {
  for (const [file, heading] of [["assign.jsx", "What needs doing?"], ["work.jsx", "Work"], ["automations.jsx", "Automations"], ["runners.jsx", "Runners"]]) {
    const source = await readFile(new URL(`./${file}`, import.meta.url), "utf8");
    assert.match(source, /className="page /, file);
    assert.ok(source.includes(heading), file);
  }
});

test("mobile navigation holds exactly the four pages", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.app-sidebar nav \{ position: fixed;[^}]*bottom: 0;/);
  assert.match(styles, /grid-template-columns: repeat\(4, minmax\(0, 1fr\)\)/);
  assert.doesNotMatch(styles, /grid-template-columns:\s*repeat\(5,/);
  assert.match(styles, /padding-bottom: calc\(4\.15rem \+ env\(safe-area-inset-bottom\)\)/);
});
