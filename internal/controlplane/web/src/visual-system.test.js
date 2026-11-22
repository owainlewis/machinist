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

test("mobile navigation holds exactly the four pages", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.app-sidebar nav \{ position: fixed;[^}]*bottom: 0;/);
  assert.match(styles, /grid-template-columns: repeat\(4, minmax\(0, 1fr\)\); gap: 0;/);
  assert.match(styles, /padding-bottom: calc\(4\.15rem \+ env\(safe-area-inset-bottom\)\)/);
});

test("the New job form has one chevron per select, one title hint, and a prompt that leaves Start job in view", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  const page = await readFile(new URL("./new-job-page.jsx", import.meta.url), "utf8");
  assert.doesNotMatch(styles, /select[^{]*\{[^}]*linear-gradient/, "a select draws no triangles of its own");
  assert.match(styles, /\.select-field select\.field-control \{ appearance: none; background-image: none;/);
  assert.equal((page.match(/<select /g) || []).length, 1, "both selects use the one Select");
  assert.equal((page.match(/The first line becomes the job's title\./g) || []).length, 1);
  assert.match(page, /rows=\{8\}/);
  assert.match(styles, /\.new-job-prompt \{ min-height: 160px;/);
});

test("pipeline nodes are one height and the running ring stays inside the scroll area", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.pipeline ol \{[^}]*align-items: stretch;[^}]*padding: 4px;/);
  assert.match(styles, /\.pipeline li \{[^}]*align-items: stretch;/);
  assert.match(styles, /\.pipeline-edge \{[^}]*align-self: center;/);
});

test("on a phone the chart leaves room for its last date", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  const phone = styles.slice(styles.indexOf("@media (max-width: 767px) {\n  .page {"));
  assert.match(phone.slice(0, phone.indexOf("\n}")), /\.chart-body, \.chart-dates \{ padding-right: 20px; \}/);
});
