import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { TasksView } from "./Tasks";
import type { ExecutionProfile, Pipeline, Task, RunDetail } from "./types";

const pipelines: Pipeline[] = [{
  id: "00000000-0000-0000-0000-000000000001",
  name: "Single agent",
  generation: 1,
  stages: [{ position: 0, name: "Do the task", prompt: "" }],
  created_at: "2026-08-11T12:00:00Z",
  updated_at: "2026-08-11T12:00:00Z",
}, {
  id: "pipeline-review",
  name: "Build and review",
  generation: 1,
  stages: [
    { position: 0, name: "Build", prompt: "" },
    { position: 1, name: "Review", prompt: "" },
  ],
  created_at: "2026-08-11T12:00:00Z",
  updated_at: "2026-08-11T12:00:00Z",
}];

const executionProfiles: ExecutionProfile[] = [{
  id: "persistent-auto",
  name: "Persistent auto",
  kind: "persistent",
  version: 1,
  runtime: "",
  provider: "worker",
  model: "worker-default",
  timeout_seconds: 0,
  resource_class: "worker",
  max_concurrent: 100,
  enabled: true,
  healthy: true,
  synthetic_worker_id: "",
}, {
  id: "profile-cloud-1",
  name: "Cloud Run test profile",
  kind: "fake_cloud_run",
  version: 1,
  runtime: "codex",
  provider: "openrouter",
  model: "deepseek/test",
  timeout_seconds: 900,
  resource_class: "standard",
  max_concurrent: 10,
  enabled: true,
  healthy: true,
  synthetic_worker_id: "cloud-run-profile-cloud-1",
}];

const task: Task = {
  id: "task-1",
  name: "Ship ready work",
  prompt: "Find ready work.",
  prompt_preview: "Find ready work.",
  runtime: "codex",
  timeout_seconds: 7200,
  concurrency_limit: 10,
  generation: 2,
  archived: false,
  read_only: false,
  repositories: [],
  repository_count: 0,
  schedule: { enabled: false, health_status: "disabled" },
  created_at: "2026-08-11T12:00:00Z",
  updated_at: "2026-08-11T12:00:00Z",
};

describe("TasksView", () => {
  beforeEach(() => {
    vi.spyOn(api, "executionProfiles").mockResolvedValue(executionProfiles);
    vi.spyOn(api, "pipelines").mockResolvedValue(pipelines);
  });

  it("reuses the Run request key after an ambiguous failure", async () => {
    const runnable = { ...task, repository_count: 1 };
    vi.spyOn(api, "tasks").mockResolvedValue([runnable]);
    const runTask = vi.spyOn(api, "runTask").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView onRun={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    const dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The response was lost.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(runTask).toHaveBeenCalledTimes(2);
    expect(runTask.mock.calls[1][1]).toBe(runTask.mock.calls[0][1]);
    expect(runTask).toHaveBeenLastCalledWith(runnable.id, expect.any(String), "persistent-auto");
  });

  it("uses a new Run request key after the Task generation changes", async () => {
    const runnable = { ...task, repository_count: 1 };
    vi.spyOn(api, "tasks").mockResolvedValue([runnable]);
    const runTask = vi.spyOn(api, "runTask").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView onRun={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    let dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The response was lost.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    client.setQueryData(["tasks", false], [{ ...runnable, name: "Updated Task", generation: runnable.generation + 1 }]);
    expect(await screen.findByText("Updated Task")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    dialog = await screen.findByRole("dialog", { name: "Run Updated Task" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(runTask).toHaveBeenCalledTimes(2);
    expect(runTask.mock.calls[1][1]).not.toBe(runTask.mock.calls[0][1]);
  });

  it("uses a new Run request key when the execution destination changes", async () => {
    const runnable = { ...task, repository_count: 1 };
    vi.spyOn(api, "tasks").mockResolvedValue([runnable]);
    const runTask = vi.spyOn(api, "runTask").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView onRun={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    let dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.selectOptions(within(dialog).getByLabelText("Run on"), "profile-cloud-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The response was lost.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));

    expect(runTask.mock.calls[0][2]).toBe("profile-cloud-1");
    expect(runTask.mock.calls[1][2]).toBe("persistent-auto");
    expect(runTask.mock.calls[1][1]).not.toBe(runTask.mock.calls[0][1]);
  });

  it("lets a manual run override the saved execution destination", async () => {
    const runnable = { ...task, repository_count: 1 };
    vi.spyOn(api, "tasks").mockResolvedValue([runnable]);
    const result = { run: { id: "run-cloud-1" }, sessions: [] } as unknown as RunDetail;
    const runTask = vi.spyOn(api, "runTask").mockResolvedValue(result);
    const onRun = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView onRun={onRun} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    const dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.selectOptions(within(dialog).getByLabelText("Run on"), "profile-cloud-1");
    expect(within(dialog).getByText("codex · openrouter / deepseek/test")).toBeVisible();
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));

    expect(runTask).toHaveBeenCalledWith(runnable.id, expect.any(String), "profile-cloud-1");
    expect(onRun).toHaveBeenCalledWith("run-cloud-1");
  });

  it("blocks a cloud override for a multi-stage Pipeline", async () => {
    const runnable = {
      ...task,
      repository_count: 1,
      pipeline_id: "pipeline-review",
      execution_profile_id: "profile-cloud-1",
    };
    vi.spyOn(api, "tasks").mockResolvedValue([runnable]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView onRun={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    const dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    expect(within(dialog).getByText("Multi-stage Pipelines require a persistent Worker.")).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Run now" })).toBeDisabled();
  });

  it("keeps the editor open and shows archive failures", async () => {
    vi.spyOn(api, "tasks").mockResolvedValue([task]);
    vi.spyOn(api, "task").mockResolvedValue(task);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    vi.spyOn(api, "archiveTask").mockRejectedValue(new Error("Task changed; refresh and try again."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView initialID={task.id} onRun={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "Edit Task" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Archive" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Task changed; refresh and try again.");
    expect(screen.getByRole("dialog", { name: "Edit Task" })).toBeVisible();
  });

  it("keeps the editor open and shows occurrence discard failures", async () => {
    const blocked: Task = {
      ...task,
      schedule: {
        enabled: true,
        cron: "0 9 * * *",
        timezone: "UTC",
        pending_due_at: "2026-08-11T09:00:00Z",
        health_status: "blocked",
        health_message: "Repository unavailable.",
      },
    };
    vi.spyOn(api, "tasks").mockResolvedValue([blocked]);
    vi.spyOn(api, "task").mockResolvedValue(blocked);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    vi.spyOn(api, "discardTaskOccurrence").mockRejectedValue(new Error("The pending occurrence changed."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView initialID={blocked.id} onRun={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "Edit Task" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Discard occurrence" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The pending occurrence changed.");
    expect(screen.getByRole("dialog", { name: "Edit Task" })).toBeVisible();
  });

  it("preserves the saved execution profile when editing a Task", async () => {
    const cloudTask: Task = { ...task, execution_profile_id: "profile-cloud-1" };
    vi.spyOn(api, "tasks").mockResolvedValue([cloudTask]);
    vi.spyOn(api, "task").mockResolvedValue(cloudTask);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    const updateTask = vi.spyOn(api, "updateTask").mockResolvedValue(cloudTask);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView initialID={cloudTask.id} onRun={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "Edit Task" });
    expect(within(dialog).getByLabelText("Run on")).toHaveValue("profile-cloud-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save Task" }));

    expect(updateTask).toHaveBeenCalledWith(cloudTask.id, expect.objectContaining({
      execution_profile_id: "profile-cloud-1",
    }));
  });

  it("saves a selected default execution destination on a new Task", async () => {
    vi.spyOn(api, "tasks").mockResolvedValue([]);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    const createTask = vi.spyOn(api, "createTask").mockResolvedValue({ ...task, execution_profile_id: "profile-cloud-1" });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView createOpen onRun={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "New Task" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "Cloud review");
    await userEvent.type(within(dialog).getByLabelText("Prompt"), "Review the repository.");
    await userEvent.selectOptions(within(dialog).getByLabelText("Run on"), "profile-cloud-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save Task" }));

    expect(createTask).toHaveBeenCalledWith(expect.objectContaining({ execution_profile_id: "profile-cloud-1" }));
  });

  it("saves the selected Pipeline on a new Task", async () => {
    vi.spyOn(api, "tasks").mockResolvedValue([]);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    const createTask = vi.spyOn(api, "createTask").mockResolvedValue({ ...task, pipeline_id: "pipeline-review" });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView createOpen onRun={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "New Task" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "Reviewed build");
    await userEvent.type(within(dialog).getByLabelText("Prompt"), "Implement the ticket.");
    await userEvent.selectOptions(within(dialog).getByLabelText("Pipeline"), "pipeline-review");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save Task" }));

    expect(createTask).toHaveBeenCalledWith(expect.objectContaining({ pipeline_id: "pipeline-review" }));
  });

  it("blocks saving a multi-stage Pipeline with a cloud profile", async () => {
    vi.spyOn(api, "tasks").mockResolvedValue([]);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView createOpen onRun={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "New Task" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "Cloud Pipeline");
    await userEvent.type(within(dialog).getByLabelText("Prompt"), "Review the repository.");
    await userEvent.selectOptions(within(dialog).getByLabelText("Run on"), "profile-cloud-1");
    await userEvent.selectOptions(within(dialog).getByLabelText("Pipeline"), "pipeline-review");

    expect(within(dialog).getByText(/Multi-stage Pipelines require a persistent Worker/)).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Save Task" })).toBeDisabled();
  });

  it("keeps Save disabled when Pipeline compatibility cannot be loaded", async () => {
    vi.spyOn(api, "tasks").mockResolvedValue([]);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    vi.mocked(api.pipelines).mockRejectedValue(new Error("Pipeline list unavailable."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><TasksView createOpen onRun={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "New Task" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "Safe Task");
    await userEvent.type(within(dialog).getByLabelText("Prompt"), "Review the repository.");

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Pipeline list unavailable.");
    expect(within(dialog).getByRole("button", { name: "Save Task" })).toBeDisabled();
  });
});
