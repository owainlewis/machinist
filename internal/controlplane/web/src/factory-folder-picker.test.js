import test from "node:test";
import assert from "node:assert/strict";
import { parentFolder, cloneDestination } from "./factory-folder-state.js";
test("folder picker derives clone destinations for local and remote hosts", () => {
  assert.equal(
    cloneDestination("/home/build", "", "git@github.com:owner/app.git", ""),
    "/home/build/app",
  );
  assert.equal(
    cloneDestination(
      "/home/build",
      "/tmp/chosen",
      "https://example.com/app.git",
      "",
    ),
    "/home/build/chosen",
  );
  const bs = String.fromCharCode(92);
  assert.equal(
    cloneDestination(
      "C:" + bs + "Code",
      "C:" + bs + "Repos" + bs + "app",
      "https://example.com/app.git",
      "",
    ),
    "C:" + bs + "Code" + bs + "app",
  );
  assert.equal(
    parentFolder("C:" + bs + "Code" + bs + "app"),
    "C:" + bs + "Code",
  );
  assert.equal(parentFolder("C:" + bs + "app"), "C:" + bs);
  assert.equal(parentFolder("/home/build/app"), "/home/build");
  assert.equal(
    cloneDestination(
      "/home/back" + bs + "slash",
      "",
      "https://example.com/app.git",
      "",
    ),
    "/home/back" + bs + "slash/app",
  );
});
