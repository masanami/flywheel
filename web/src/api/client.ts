import { ApiError, type ProposedApi } from "./proposed.ts";
import type {
  ApprovalSummaryResponse,
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

// 失敗の応答 {"error":{"code","message"}} を ApiError にする。読めない本文は status だけを持つ ApiError にする。
async function failure(res: Response, what: string): Promise<ApiError> {
  let code = "internal_error";
  let message = `${what}: ${res.status}`;
  try {
    const body = (await res.json()) as {
      error?: { code?: unknown; message?: unknown };
    } | null;
    if (typeof body?.error?.code === "string") code = body.error.code;
    if (typeof body?.error?.message === "string") message = body.error.message;
  } catch {
    // 本文が JSON でない失敗（逆プロキシの応答など）は status だけで扱う
  }
  return new ApiError(code, message, res.status);
}

// 読み取りは GET（SSE は EventSource）。書き込みは下の post だけで、呼ぶのは承認と 1 周の起動の POST に限る。
async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, {
    method: "GET",
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw await failure(res, `GET ${path}`);
  }
  return (await res.json()) as T;
}

// サーバが index.html に埋め込む CSRF の値。無ければ空のまま送り、サーバの 403 で拒否させる。
function csrfToken(): string {
  return (
    document
      .querySelector('meta[name="flywheel-csrf"]')
      ?.getAttribute("content") ?? ""
  );
}

// すべての POST は CSRF の値を X-Flywheel-CSRF に載せる。自動の再送はしない（呼び出し側も再送しない）。
export async function post<T>(path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    "X-Flywheel-CSRF": csrfToken(),
  };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, {
    method: "POST",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw await failure(res, `POST ${path}`);
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

// ボードのボタンでの承認（M1 の 2 段階）。① 要約と版を得る → ② 版を添えて承認する。
export interface ApprovalApi {
  approvalSummary(name: string, id: string): Promise<ApprovalSummaryResponse>;
  approve(name: string, id: string, version: number): Promise<ShowCardResponse>;
}

export type ApiClient = ReadApi & ApprovalApi & ProposedApi;

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

const approvalApi: ApprovalApi = {
  approvalSummary: (name, id) =>
    get<ApprovalSummaryResponse>(
      `${ws(name)}/challenges/${encodeURIComponent(id)}/approval`,
    ),
  approve: (name, id, version) =>
    post<ShowCardResponse>(
      `${ws(name)}/challenges/${encodeURIComponent(id)}/approve`,
      { version },
    ),
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
  ...approvalApi,
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
  approvalSummary: (n, id) => impl.approvalSummary(n, id),
  approve: (n, id, v) => impl.approve(n, id, v),
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
