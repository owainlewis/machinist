import test from "node:test";
import assert from "node:assert/strict";
import {
  clampPanelWidth,
  initialReviewTab,
  inlineMarkdown,
  markdownBlocks,
  parseDiff,
  safeLink,
} from "./factory-review.js";
test("unified diff groups files and binds old/new line numbers", () => {
  const files = parseDiff(
    "\ndiff --git a/main.js b/main.js\n--- a/main.js\n+++ b/main.js\n@@ -5,2 +5,2 @@\n const x = 1;\n-old();\n+new();\ndiff --git a/style.css b/style.css\n--- a/style.css\n+++ b/style.css\n@@ -0,0 +1 @@\n+body {}\n",
  );
  assert.equal(files.length, 2);
  assert.equal(files[0].path, "main.js");
  assert.equal(files[0].added, 1);
  assert.equal(files[0].deleted, 1);
  assert.equal(files[0].lines[2].old, 6);
  assert.equal(files[0].lines[3].new, 6);
  assert.equal(files[1].lines[1].new, 1);
  assert.deepEqual(parseDiff(""), []);
  assert.deepEqual(parseDiff("\n\n"), []);
});
test("markdown keeps raw HTML literal and permits only safe link protocols", () => {
  assert.equal(safeLink("javascript:alert(1)"), null);
  assert.equal(safeLink("data:text/html,<script>"), null);
  assert.equal(safeLink("https://example.com"), "https://example.com");
  const blocks = markdownBlocks(
    "# Plan\n\n<script>alert(1)</script>\n\n- **Test** the change\n- Read `main.js`\n\n```js\n<script>literal</script>\n```",
  );
  assert.equal(blocks[0].type, "heading");
  assert.equal(blocks[1].text, "<script>alert(1)</script>");
  assert.equal(blocks[2].items.length, 2);
  assert.equal(blocks[3].type, "code");
  assert.equal(inlineMarkdown("[unsafe](javascript:alert)")[0].type, "text");
  assert.equal(inlineMarkdown("[docs](https://example.com)")[0].type, "link");
});
test("review defaults follow stage and failures; widths stay usable", () => {
  assert.equal(initialReviewTab({ stage: "Design" }), "design");
  assert.equal(initialReviewTab({ stage: "Review" }), "changes");
  assert.equal(
    initialReviewTab({
      stage: "Build",
      status: "failed",
      checks: [{ passed: false }],
    }),
    "checks",
  );
  assert.equal(clampPanelWidth(100, 1200), 320);
  assert.equal(clampPanelWidth(2000, 1200), 1000);
  assert.equal(clampPanelWidth(1000, 800), 720);
});

test("hunk content resembling file headers stays visible and quoted paths decode", () => {
  const marker = parseDiff(
    "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n--- marker\n+++ marker",
  );
  assert.equal(marker[0].path, "a.txt");
  assert.equal(marker[0].deleted, 1);
  assert.equal(marker[0].added, 1);
  assert.equal(marker[0].lines[1].text, "--- marker");
  assert.equal(marker[0].lines[2].text, "+++ marker");
  const quoted = parseDiff(
    'diff --git "a/caf\\303\\251\\tfile.txt" "b/caf\\303\\251\\tfile.txt"\n--- "a/caf\\303\\251\\tfile.txt"\n+++ "b/caf\\303\\251\\tfile.txt"\n@@ -1 +1 @@\n-a\n+b',
  );
  assert.equal(quoted[0].path, "café\tfile.txt");
});

test("space filename headers ignore separator tabs without losing actual quoted tabs", () => {
  const files = parseDiff(
    "diff --git a/with spaces.txt b/with spaces.txt\n--- a/with spaces.txt\t\n+++ b/with spaces.txt\t\n@@ -1 +1 @@\n-a\n+b",
  );
  assert.equal(files[0].path, "with spaces.txt");
});
