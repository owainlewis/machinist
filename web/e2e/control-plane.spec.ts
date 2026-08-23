import { expect, test } from "@playwright/test";

test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

const taskName = "E2E repository review";
const pipelineName = "Plan, build, review";

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    const response = await request.get("/api/v1/repositories");
    if (!response.ok()) return 0;
    const body = await response.json() as { repositories?: unknown[] };
    return body.repositories?.length ?? 0;
  }, { timeout: 30_000 }).toBeGreaterThan(0);
});

test("creates a reusable Pipeline in the visual editor", async ({ page }) => {
  await page.goto("/pipelines");
  await expect(page.getByRole("heading", { name: "Pipelines", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "New Pipeline", exact: true }).first().click();
  const dialog = page.getByRole("dialog", { name: "New Pipeline" });
  await dialog.getByLabel("Name", { exact: true }).fill(pipelineName);
  await dialog.getByLabel("Stage name", { exact: true }).fill("Plan");
  await dialog.locator("textarea").fill("Plan this work:\n{{ task.prompt }}");
  await dialog.getByRole("button", { name: "Add agent stage" }).click();
  await dialog.getByLabel("Stage name", { exact: true }).nth(1).fill("Build");
  await dialog.locator("textarea").nth(1).fill("Build on {{ branch }} in {{ repository }}.");
  await dialog.getByRole("button", { name: "Add agent stage" }).click();
  await dialog.getByLabel("Stage name", { exact: true }).nth(2).fill("Review");
  await dialog.locator("textarea").nth(2).fill("Review {{ task.name }} and report the result.");
  await dialog.getByRole("button", { name: "Save Pipeline" }).click();

  const card = page.getByRole("button", { name: new RegExp(pipelineName) });
  await expect(card).toContainText("3 agent starts per repository");
  await expect(card).toContainText("Plan");
  await expect(card).toContainText("Build");
  await expect(card).toContainText("Review");

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(card).toBeVisible();
  expect(await page.evaluate<boolean>("document.documentElement.scrollWidth <= document.documentElement.clientWidth")).toBe(true);
});

test("creates a Task and completes its Run", async ({ page }) => {
  await page.goto("/tasks");
  await expect(page.getByRole("heading", { name: "Tasks", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Definitions" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Automations" })).toHaveCount(0);

  await page.getByRole("button", { name: "New Task", exact: true }).first().click();
  const dialog = page.getByRole("dialog", { name: "New Task" });
  await expect(dialog.getByText("Tools", { exact: true })).toHaveCount(0);
  await dialog.getByLabel("Name").fill(taskName);
  await dialog.getByLabel("Prompt").fill("Review this repository and leave deterministic browser evidence.");
  await dialog.getByLabel("Pipeline").selectOption({ label: `${pipelineName} · 3 stages` });
  await dialog.locator(".repository-picker button").first().click();
  await dialog.getByRole("button", { name: "Save Task" }).click();

  const task = page.locator("article").filter({ hasText: taskName });
  await expect(task).toContainText("1 repos");
  await task.getByRole("button", { name: "Run now" }).click();
  const runDialog = page.getByRole("dialog", { name: `Run ${taskName}` });
  await expect(runDialog.getByLabel("Run on")).toHaveValue("persistent-auto");
  await runDialog.getByRole("button", { name: "Run now" }).click();

  await expect(page).toHaveURL(/\/work\/[0-9a-f-]+$/);
  await expect(page.getByRole("heading", { name: taskName })).toBeVisible();
  await expect(page.locator(".run-detail-heading").getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 45_000 });
  const runSummary = page.locator(".run-summary-strip");
  await expect(runSummary).toContainText(pipelineName);
  await expect(runSummary.locator("div").filter({ hasText: "Completed" })).toContainText("1");
  await page.locator(".session-row summary").click();
  await expect(page.locator(".stage-run")).toHaveCount(3);
  await expect(page.locator(".stage-run").nth(0)).toContainText("Plan");
  await expect(page.locator(".stage-run").nth(1)).toContainText("Build");
  await expect(page.locator(".stage-run").nth(2)).toContainText("Review");
  await expect(page.locator(".stage-run-succeeded")).toHaveCount(3);
  await expect(page.getByText("Completed by deterministic fake Codex.", { exact: false })).toBeVisible();
  await expect(page.getByText("Attempt 1", { exact: true })).toBeVisible();
  await expect(page.locator(".attempt-events")).toContainText("Inspected the assigned repository.");
});

test("makes the board the primary Work view and preserves the table view", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Work", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Work summary" })).toBeVisible();
  const completedRun = page.getByRole("region", { name: "Done" }).getByRole("button", { name: new RegExp(taskName) });
  await expect(completedRun).toBeVisible();
  await expect(completedRun).toContainText("factory-demo");
  await expect(completedRun).toContainText("1/1");
  expect(await page.evaluate<boolean>("document.documentElement.scrollWidth <= document.documentElement.clientWidth")).toBe(true);

  await expect(page.getByRole("button", { name: "List", exact: true })).toHaveCount(0);

  await page.getByRole("button", { name: "Table", exact: true }).click();
  await expect(page).toHaveURL(/\/work\?view=table/);

  await page.goto("/work?view=list");
  await expect(page.getByRole("button", { name: "Table", exact: true })).toHaveAttribute("aria-pressed", "true");

  await page.goto("/runs");
  await expect(page.getByRole("heading", { name: "Work", exact: true })).toBeVisible();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("button", { name: "Toggle navigation" })).toBeVisible();
  const board = page.locator(".work-board");
  await expect(board).toBeVisible();
  expect(await page.evaluate<boolean>("document.querySelector('.work-board').scrollWidth > document.querySelector('.work-board').clientWidth")).toBe(true);
  expect(await page.evaluate<boolean>("document.documentElement.scrollWidth <= document.documentElement.clientWidth")).toBe(true);
});

test("keeps Overview operational and the product navigation small", async ({ page }) => {
  await page.goto("/overview");
  await expect(page.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
  await expect(page.getByText("Active runs", { exact: true })).toBeVisible();
  await expect(page.getByText("Completed · 24h", { exact: true })).toBeVisible();
  const performance = page.getByRole("region", { name: "Run performance" });
  await expect(performance.getByText("Runs", { exact: true })).toBeVisible();
  await expect(performance.getByText("Completion rate", { exact: true })).toBeVisible();
  await expect(performance.getByText("Average cycle time", { exact: true })).toBeVisible();
  await expect(performance).toContainText("1 completed");
  const navigation = page.getByRole("navigation", { name: "Primary navigation" });
  await expect(navigation.getByRole("button")).toHaveCount(6);
  await expect(navigation.getByRole("group", { name: "Infrastructure" }).getByRole("button")).toHaveText([
    "Workers",
    "Repositories",
  ]);
  await expect(page.getByText(taskName, { exact: true })).toBeVisible();

  await page.setViewportSize({ width: 390, height: 560 });
  await expect(navigation.getByRole("button", { name: "Overview", exact: true })).toBeHidden();
  await page.keyboard.press("Tab");
  const mobileMenu = page.getByRole("button", { name: "Toggle navigation" });
  await expect(mobileMenu).toBeFocused();
  await mobileMenu.click();
  await expect(mobileMenu).toHaveAttribute("aria-expanded", "true");
  await expect(navigation.getByRole("button", { name: "Overview", exact: true })).toBeVisible();
});
