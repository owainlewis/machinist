import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("every top-level page uses the shared top bar", async () => {
  const files = ["main.jsx", "catalog.jsx", "triggers.jsx", "settings.jsx"];
  const sources = await Promise.all(files.map((file) => readFile(new URL(`./${file}`, import.meta.url), "utf8")));
  const all = sources.join("\n");
  for (const title of ["Home", "Tasks", "Workers", "Automations", "Settings"]) assert.match(all, new RegExp(`<TopBar title="${title}"`));
  assert.doesNotMatch(all, /PageHeading/);
});

test("mobile navigation becomes a five-item bottom bar", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");

  assert.match(styles, /\.app-sidebar \{ position: fixed;[^}]*bottom: 0;/);
  assert.match(styles, /grid-template-columns: repeat\(5, minmax\(0, 1fr\)\)/);
  assert.match(styles, /padding: 0 0 calc\(3\.75rem \+ env\(safe-area-inset-bottom\)\)/);
});

test("running spinners respect reduced motion", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");

  assert.match(styles, /\.spinner \{ animation: machinist-spin/);
  assert.match(styles, /prefers-reduced-motion: reduce\)[\s\S]*\.spinner \{ animation: machinist-pulse/);
});

test("the build never scans its own committed output", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /@source not "\.\.\/dist";/);
});
