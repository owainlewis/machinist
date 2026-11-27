import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("every page has one heading in the shared page frame", async () => {
  for (const [file, heading] of [["jobs-page.jsx", "Jobs"], ["job-page.jsx", "<h1>"], ["automations-view.jsx", "Automations"], ["runners.jsx", "Runners"], ["dashboard-page.jsx", "Dashboard"]]) {
    const source = await readFile(new URL(`./${file}`, import.meta.url), "utf8");
    assert.match(source, /className="page /, file);
    assert.ok(source.includes(heading), file);
  }
});

test("mobile navigation holds exactly the five pages", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.app-sidebar nav \{ position: fixed;[^}]*bottom: 0;/);
  assert.match(styles, /grid-template-columns: repeat\(5, minmax\(0, 1fr\)\); gap: 0;/);
  assert.match(styles, /padding-bottom: calc\(4\.15rem \+ env\(safe-area-inset-bottom\)\)/);
});

test("the New job form has one chevron per select, one title hint, and a prompt that leaves Start job in view", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  const page = await readFile(new URL("./new-job-page.jsx", import.meta.url), "utf8");
  const select = await readFile(new URL("./components/ui/select.jsx", import.meta.url), "utf8");
  assert.doesNotMatch(styles, /select[^{]*\{[^}]*linear-gradient/, "a select draws no triangles of its own");
  assert.match(styles, /\.select-field select\.field-control \{ appearance: none; background-image: none;/);
  assert.equal((page.match(/<select /g) || []).length, 0, "both selects use the one Select");
  assert.equal((select.match(/<select /g) || []).length, 1);
  assert.match(page, /import \{ Select \} from "@\/components\/ui\/select";/);
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

test("dashboard spend and model tables keep mobile spend columns and left-align the model header", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.model-table th\.model-name-col[^{]*\{ text-align: left; \}/);
  const phone = styles.slice(styles.indexOf("@media (max-width: 767px) {\n  .page {"));
  const phoneBlock = phone.slice(0, phone.indexOf("\n}"));
  assert.match(phoneBlock, /\.group-table:not\(\.spend-group-table\) th:nth-child\(3\)/);
  assert.doesNotMatch(phoneBlock, /\.group-table (?:th|td):nth-child\([34]\)/);
});

test("settings sections constrain wide preset pipelines so they scroll inside their card", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.settings-section \{[^}]*grid-template-columns: minmax\(0, 1fr\);/);
  assert.match(styles, /\.token-revoked-section \{[^}]*grid-template-columns: minmax\(0, 1fr\);/);
  assert.match(styles, /\.preset-cards \{[^}]*min-width: 0;/);
});
