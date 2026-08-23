import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { api } from "./api";
import { PipelinesView } from "./Pipelines";
import type { Pipeline } from "./types";

function renderView() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><PipelinesView /></QueryClientProvider>);
  return client;
}

it("creates and reorders a sequence of agent stages", async () => {
  vi.spyOn(api, "pipelines").mockResolvedValue([]);
  const create = vi.spyOn(api, "createPipeline").mockResolvedValue({ id: "pipeline-1" } as Pipeline);
  renderView();

  await userEvent.click((await screen.findAllByRole("button", { name: "New Pipeline" }))[0]);
  const dialog = await screen.findByRole("dialog", { name: "New Pipeline" });
  await userEvent.type(within(dialog).getByLabelText("Name"), "Build and review");
  await userEvent.clear(within(dialog).getByLabelText("Stage name"));
  await userEvent.type(within(dialog).getByLabelText("Stage name"), "Build");
  await userEvent.click(within(dialog).getByRole("button", { name: "Add agent stage" }));
  const stageNames = within(dialog).getAllByLabelText("Stage name");
  await userEvent.clear(stageNames[1]);
  await userEvent.type(stageNames[1], "Review");
  await userEvent.click(within(dialog).getByRole("button", { name: "Move Review up" }));
  await userEvent.click(within(dialog).getByRole("button", { name: "Save Pipeline" }));

  expect(create).toHaveBeenCalledWith({
    name: "Build and review",
    stages: [
      { name: "Review", prompt: "Review the work for this task:\n{{ task.prompt }}" },
      { name: "Build", prompt: "{{ task.prompt }}" },
    ],
  });
});

it("loads full prompt details before editing a Pipeline", async () => {
  const summary: Pipeline = {
    id: "pipeline-1", name: "Review", generation: 2,
    stages: [{ position: 0, name: "Review", prompt: "" }],
    created_at: "2026-08-11T12:00:00Z", updated_at: "2026-08-11T12:00:00Z",
  };
  vi.spyOn(api, "pipelines").mockResolvedValue([summary]);
  vi.spyOn(api, "pipeline").mockResolvedValue({ ...summary, stages: [{ ...summary.stages[0], prompt: "Review {{ task.prompt }}" }] });
  const updated = { ...summary, generation: 3, name: "Reviewed" };
  const update = vi.spyOn(api, "updatePipeline").mockResolvedValue(updated);
  const client = renderView();

  await userEvent.click(await screen.findByRole("button", { name: /Review/ }));
  const dialog = await screen.findByRole("dialog", { name: "Edit Pipeline" });
  expect(within(dialog).getByLabelText("Prompt template")).toHaveValue("Review {{ task.prompt }}");
  await userEvent.click(within(dialog).getByRole("button", { name: "Save Pipeline" }));

  expect(update).toHaveBeenCalledWith("pipeline-1", {
    name: "Review",
    stages: [{ name: "Review", prompt: "Review {{ task.prompt }}" }],
    expected_generation: 2,
  });
  await waitFor(() => expect(client.getQueryData(["pipeline", "pipeline-1"])).toEqual(updated));
});

it("requires a second click before deleting an unused Pipeline", async () => {
  const pipeline: Pipeline = {
    id: "pipeline-delete", name: "Disposable", generation: 1,
    stages: [{ position: 0, name: "Review", prompt: "Review {{ task.prompt }}" }],
    created_at: "2026-08-11T12:00:00Z", updated_at: "2026-08-11T12:00:00Z",
  };
  vi.spyOn(api, "pipelines").mockResolvedValue([{ ...pipeline, stages: [{ ...pipeline.stages[0], prompt: "" }] }]);
  vi.spyOn(api, "pipeline").mockResolvedValue(pipeline);
  const remove = vi.spyOn(api, "deletePipeline").mockResolvedValue();
  renderView();

  await userEvent.click(await screen.findByRole("button", { name: /Disposable/ }));
  const dialog = await screen.findByRole("dialog", { name: "Edit Pipeline" });
  await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
  expect(remove).not.toHaveBeenCalled();
  await userEvent.click(within(dialog).getByRole("button", { name: "Confirm delete" }));

  expect(remove).toHaveBeenCalledWith("pipeline-delete");
});
