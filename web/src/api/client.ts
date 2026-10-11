import type {
  ListCardResponse,
  NeedsHumanResponse,
  QuestionsResponse,
  StatusResponse,
  WorkspaceChanged,
  WorkspaceList,
} from "./types.gen.ts";

// UI が出す要求は GET だけ。ここ以外で fetch を呼ばない（AC-123）。
async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, {
    method: "GET",
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`GET ${path}: ${res.status}`);
  }
  return (await res.json()) as T;
}

const ws = (name: string) => `/api/v1/workspaces/${encodeURIComponent(name)}`;

export const api = {
  workspaces: () => get<WorkspaceList>("/api/v1/workspaces"),
  status: (name: string) => get<StatusResponse>(`${ws(name)}/status`),
  challenges: (name: string) => get<ListCardResponse>(`${ws(name)}/challenges`),
  needsHuman: () => get<NeedsHumanResponse>("/api/v1/needs-human"),
  questions: () => get<QuestionsResponse>("/api/v1/questions"),
};

export const eventsPath = "/api/v1/events";

// SSE の 1 通を読む。workspace_changed 以外は null。
export function parseChanged(data: string): WorkspaceChanged | null {
  try {
    const v = JSON.parse(data) as Partial<WorkspaceChanged> | null;
    if (
      v &&
      v.type === "workspace_changed" &&
      typeof v.workspace === "string"
    ) {
      return { type: v.type, workspace: v.workspace };
    }
  } catch {
    // keep-alive 以外の壊れた通知は無視する
  }
  return null;
}
