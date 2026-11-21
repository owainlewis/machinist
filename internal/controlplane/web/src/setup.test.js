import assert from "node:assert/strict";
import test from "node:test";
import { missingSteps, repoName, setupLine, shouldOpenSetup } from "./setup.js";

const checks = (...states) => ({ checks: states.map(([name, ok, required = true]) => ({ name, ok, required })) });

test("only required checks that fail are missing", () => {
  const setup = checks(["GitHub", true], ["Runner", false], ["Linear", false, false]);
  assert.deepEqual(missingSteps(setup).map(check => check.name), ["Runner"]);
  assert.equal(setupLine(setup), "1 of 2 required steps are done.");
  assert.equal(setupLine(checks(["GitHub", true], ["Linear", false, false])), "Machinist is ready to take jobs.");
});

test("a pasted GitHub link becomes owner/name", () => {
  assert.equal(repoName(" https://github.com/owner/repo/ "), "owner/repo");
  assert.equal(repoName("owner/repo.git"), "owner/repo");
  assert.equal(repoName("owner/my.repo"), "owner/my.repo");
  for (const bad of ["", "owner", "owner/", "a/b/c", "owner/re po"]) assert.equal(repoName(bad), "", bad);
});

test("a new install opens on setup, but a deep link stays where it points", () => {
  const unready = checks(["Runner", false]);
  assert.equal(shouldOpenSetup(unready, ""), true);
  assert.equal(shouldOpenSetup(unready, "#/dashboard"), true);
  assert.equal(shouldOpenSetup(unready, "#/jobs/t_1"), false);
  assert.equal(shouldOpenSetup(checks(["Runner", true]), ""), false);
});
