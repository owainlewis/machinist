import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("every page has one heading in the shared page frame", async () => {
  for (const [file, heading] of [["jobs-page.jsx", "Jobs"], ["job-page.jsx", "<h1>"], ["runners.jsx", "Runners"], ["dashboard-page.jsx", "Dashboard"]]) {
    const source = await readFile(new URL(`./${file}`, import.meta.url), "utf8");
    assert.match(source, /className="page /, file);
    assert.ok(source.includes(heading), file);
  }
});

test("mobile navigation holds exactly the three pages", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.app-sidebar nav \{ position: fixed;[^}]*bottom: 0;/);
  assert.match(styles, /grid-template-columns: repeat\(3, minmax\(0, 1fr\)\); gap: 0;/);
  assert.match(styles, /padding-bottom: calc\(4\.15rem \+ env\(safe-area-inset-bottom\)\)/);
});
