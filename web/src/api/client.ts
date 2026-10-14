import { ApiError, type ProposedApi } from "./proposed.ts";
import type {
  ListCardResponse,
  LogResponse,
  NeedsHumanResponse,
  QuestionsResponse,
  RunReportResponse,
  ShowCardResponse,
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

export interface ReadApi {
  workspaces(): Promise<WorkspaceList>;
  status(name: string): Promise<StatusResponse>;
  challenges(name: string): Promise<ListCardResponse>;
  challenge(name: string, id: string): Promise<ShowCardResponse>;
  log(name: string, id: string): Promise<LogResponse>;
  runReport(name: string, runId: string): Promise<RunReportResponse>;
  needsHuman(): Promise<NeedsHumanResponse>;
  questions(): Promise<QuestionsResponse>;
}

export type ApiClient = ReadApi & ProposedApi;

const readApi: ReadApi = {
  workspaces: () => get<WorkspaceList>("/api/v1/workspaces"),
  status: (name) => get<StatusResponse>(`${ws(name)}/status`),
  challenges: (name) => get<ListCardResponse>(`${ws(name)}/challenges`),
  challenge: (name, id) =>
    get<ShowCardResponse>(`${ws(name)}/challenges/${encodeURIComponent(id)}`),
  log: (name, id) =>
    get<LogResponse>(`${ws(name)}/log?challenge=${encodeURIComponent(id)}`),
  runReport: (name, runId) =>
    get<RunReportResponse>(
      `${ws(name)}/runs/${encodeURIComponent(runId)}/report`,
    ),
  needsHuman: () => get<NeedsHumanResponse>("/api/v1/needs-human"),
  questions: () => get<QuestionsResponse>("/api/v1/questions"),
};

// 案の口（proposed.ts）は server にまだ無い。本物の呼び出しは何も送らずに失敗させる。
const notBuilt = (): Promise<never> =>
  Promise.reject(
    new ApiError(
      "internal_error",
      "この API はまだありません（開発時の mock モードで確かめてください）",
      501,
    ),
  );

const realApi: ApiClient = {
  ...readApi,
  cycleInfo: notBuilt,
  challengeGraph: notBuilt,
  agentProfiles: notBuilt,
};

// 呼び出し口は差し替えられる。開発時の mock モードだけが setApi でブラウザ内の偽の API を入れる。
let impl: ApiClient = realApi;
export function setApi(next: ApiClient): void {
  impl = next;
}
export function resetApi(): void {
  impl = realApi;
}

// 呼び出し側は常に `api.xxx` と書く。差し替えは呼び出しの時点で効く。
export const api: ApiClient = {
  workspaces: () => impl.workspaces(),
  status: (n) => impl.status(n),
  challenges: (n) => impl.challenges(n),
  challenge: (n, id) => impl.challenge(n, id),
  log: (n, id) => impl.log(n, id),
  runReport: (n, r) => impl.runReport(n, r),
  needsHuman: () => impl.needsHuman(),
  questions: () => impl.questions(),
  cycleInfo: (n) => impl.cycleInfo(n),
  challengeGraph: (n) => impl.challengeGraph(n),
  agentProfiles: () => impl.agentProfiles(),
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
