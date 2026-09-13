import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("runs default to table view and both view controls update the selection", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");

  assert.match(source, /const \[runsView, setRunsView\] = useState\("table"\)/);
  assert.match(source, /aria-pressed=\{runsView === "board"\} onClick=\{\(\) => setRunsView\("board"\)\}/);
  assert.match(source, /aria-pressed=\{runsView === "table"\} onClick=\{\(\) => setRunsView\("table"\)\}/);
  assert.match(source, /\{runsView === "board" \? <RunBoard jobs=\{visibleJobs\} \/> : <Card/);
});
