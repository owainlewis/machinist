import type {
  APIErrorBody,
  AttemptEventPage,
  ExecutionProfile,
  ManagedRepository,
  ManagedRepositoryReadiness,
  Task,
  Overview,
  SaveTaskInput,
  Pipeline,
  SavePipelineInput,
  RunDetail,
  RunPage,
  Worker,
} from "./types";

export class APIError extends Error {
  constructor(public code: string, message: string, public status: number) {
    super(message);
    this.name = "APIError";
  }
}

async function requestWithStatus<T>(path: string, init?: RequestInit): Promise<{ data: T; status: number }> {
  const response = await fetch(path, {
    ...init,
    headers: init?.body ? { "Content-Type": "application/json", ...init.headers } : init?.headers,
  });
  if (!response.ok) {
    let body: APIErrorBody | undefined;
    try {
      body = await response.json() as APIErrorBody;
    } catch {
      // A proxy response may not be JSON. The HTTP status remains useful.
    }
    throw new APIError(
      body?.error.code ?? "request_failed",
      body?.error.message ?? `Request failed with status ${response.status}`,
      response.status,
    );
  }
  if (response.status === 204) return { data: undefined as T, status: response.status };
  return { data: await response.json() as T, status: response.status };
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  return (await requestWithStatus<T>(path, init)).data;
}

export const api = {
  overview: () => request<Overview>("/api/v1/overview"),
  executionProfiles: async () => (await request<{ profiles: ExecutionProfile[] | null }>("/api/v1/execution-profiles")).profiles ?? [],
  pipelines: async () => (await request<{ pipelines: Pipeline[] | null }>("/api/v1/pipelines")).pipelines ?? [],
  pipeline: (id: string) => request<Pipeline>(`/api/v1/pipelines/${encodeURIComponent(id)}`),
  createPipeline: (input: SavePipelineInput) => request<Pipeline>("/api/v1/pipelines", {
    method: "POST", body: JSON.stringify(input),
  }),
  updatePipeline: (id: string, input: SavePipelineInput) => request<Pipeline>(`/api/v1/pipelines/${encodeURIComponent(id)}`, {
    method: "PUT", body: JSON.stringify(input),
  }),
  deletePipeline: (id: string) => request<void>(`/api/v1/pipelines/${encodeURIComponent(id)}`, { method: "DELETE" }),
  tasks: async (includeArchived = false) => {
    const tasks: Task[] = [];
    let cursor = "";
    do {
      const query = new URLSearchParams({ limit: "200", include_archived: String(includeArchived) });
      if (cursor) query.set("cursor", cursor);
      const page = await request<{ tasks: Task[] | null; next_cursor?: string }>(`/api/v1/tasks?${query}`);
      tasks.push(...(page.tasks ?? []));
      cursor = page.next_cursor ?? "";
    } while (cursor);
    return tasks;
  },
  task: (id: string) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}`),
  createTask: (input: SaveTaskInput) => request<Task>("/api/v1/tasks", {
    method: "POST", body: JSON.stringify(input),
  }),
  updateTask: (id: string, input: SaveTaskInput) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}`, {
    method: "PUT", body: JSON.stringify(input),
  }),
  archiveTask: (id: string, archived: boolean, expectedGeneration: number) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}/archived`, {
    method: "PUT", body: JSON.stringify({ archived, expected_generation: expectedGeneration }),
  }),
  runTask: (id: string, requestKey: string, executionProfileID?: string) => request<RunDetail>(`/api/v1/tasks/${encodeURIComponent(id)}/run`, {
    method: "POST", body: JSON.stringify({ request_key: requestKey, execution_profile_id: executionProfileID }),
  }),
  discardTaskOccurrence: (id: string, pendingDueAt: string) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}/discard-occurrence`, {
    method: "POST", body: JSON.stringify({ pending_due_at: pendingDueAt }),
  }),
  runs: async (cursor = "") => {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<RunPage>(`/api/v1/runs?${query}`);
    return { runs: page.runs ?? [], next_cursor: page.next_cursor ?? null };
  },
  run: (id: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}`),
  cancelRun: (id: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}/cancel`, {
    method: "POST", body: "{}",
  }),
  retrySession: (runId: string, sessionId: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(sessionId)}/retry`, {
    method: "POST", body: "{}",
  }),
  events: async (attemptID: string, after: number): Promise<AttemptEventPage> => {
    const query = new URLSearchParams({ after: String(after), limit: "100" });
    const page = await request<{ events: AttemptEventPage["events"] | null; next_after: number; has_more: boolean }>(
      `/api/v1/attempts/${encodeURIComponent(attemptID)}/events?${query}`,
    );
    return { ...page, events: page.events ?? [] };
  },
  cancelSession: (runId: string, sessionId: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(sessionId)}/cancel`, {
    method: "POST", body: "{}",
  }),
  workers: async () => ((await request<{ workers: Worker[] | null }>("/api/v1/workers")).workers ?? []).map(normalizeWorker),
  worker: async (id: string) => normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}`)),
  testWorker: async (id: string) => normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}/test`, {
    method: "POST", body: "{}",
  })),
  repositories: async () => (await request<{ repositories: ManagedRepository[] | null }>("/api/v1/repositories")).repositories ?? [],
  repository: (id: string) => request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}`),
  repositoryReadiness: (id: string) => request<ManagedRepositoryReadiness>(`/api/v1/repositories/${encodeURIComponent(id)}/readiness`),
  createRepository: async (remoteIdentity: string) => {
    const response = await requestWithStatus<ManagedRepository>("/api/v1/repositories", {
      method: "POST", body: JSON.stringify({ remote_identity: remoteIdentity }),
    });
    return { repository: response.data, created: response.status === 201 };
  },
  setRepositoryEnabled: (id: string, enabled: boolean) => request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}/enabled`, {
    method: "PUT", body: JSON.stringify({ enabled }),
  }),
};

function normalizeWorker(worker: Worker): Worker {
  const capabilities = worker.capabilities?.length ? worker.capabilities : [{
    kind: "runtime" as const,
    name: worker.runtime,
    status: worker.health === "healthy" ? "ready" as const : "unhealthy" as const,
    version: worker.runtime_version,
  }];
  return {
    ...worker,
    labels: worker.labels ?? {},
    capabilities,
    repositories: worker.repositories ?? [],
    retained_worktrees: worker.retained_worktrees ?? [],
    source_access: worker.source_access ?? [],
  };
}
