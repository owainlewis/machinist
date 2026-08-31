import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("every top-level control plane view uses the shared editorial heading", async () => {
  const [main, analytics, catalog, triggers] = await Promise.all([
    readFile(new URL("./main.jsx", import.meta.url), "utf8"),
    readFile(new URL("./analytics.jsx", import.meta.url), "utf8"),
    readFile(new URL("./catalog.jsx", import.meta.url), "utf8"),
    readFile(new URL("./triggers.jsx", import.meta.url), "utf8"),
  ]);

  assert.match(main, /<PageHeading title="Runs"/);
  assert.match(analytics, /<PageHeading title="Task analytics"/);
  assert.match(catalog, /<Page title="Workers"/);
  assert.match(catalog, /<Page title="Commands"/);
  assert.match(triggers, /<PageHeading title=\{title\}/);
  assert.doesNotMatch([main, analytics, catalog, triggers].join("\n"), /Control plane \/|index="0[1-5]"/);
});

test("mobile navigation has a dedicated bottom navigation treatment", async () => {
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");

  assert.match(styles, /\.app-sidebar nav \{ position: fixed;[^}]*bottom: 0;/);
  assert.match(styles, /grid-template-columns: repeat\(5, minmax\(0, 1fr\)\)/);
  assert.match(styles, /padding-bottom: 4\.15rem/);
});
