import { ApiError } from "./error.ts";
import type {
  ApprovalSummaryResponse,
  ChatPostRequest,
  ChatPostResponse,
  ChatPromptAnswerRequest,
  ChatResponse,
  ChatSessionsResponse,
  ChatTranscriptResponse,
  CycleScreen,
  CycleStart,
  CycleStarted,
  CycleStopResponse,
  FollowupDoneRequest,
  FollowupDropRequest,
  FollowupListResponse,
  HoldAnswerRequest,
  HoldAnswerResponse,
  IngestAcceptedResponse,
  IngestRequest,
  ListCardResponse,
  LogResponse,
  NeedsHumanResponse,
  NoticesResponse,
  QuestionsResponse,
  RunReportResponse,
  ShowCardResponse,
  StatusResponse,
  TreeResponse,
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

// 読み取りは GET（SSE は EventSource）。書き込みは下の post だけで、呼ぶのは計画の承認・完了の承認・「サイクルを回す」・対話の欄・画面のボタン（選ぶだけの回答・取り込み・サイクルを止める・フォローアップを済みにする／捨てる）の POST に限る。
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
  // 中身の無い成功（204。対話の「止める」「新しい会話」）は読まずに返す。
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

const ws = (name: string) => `/api/v1/workspaces/${encodeURIComponent(name)}`;

export interface ReadApi {
  workspaces(): Promise<WorkspaceList>;
  status(name: string): Promise<StatusResponse>;
  // 取り込めるもの（上流の読み取り）を含む状況。「いま進められること」の画面だけが呼ぶ。
  statusIngestable(name: string): Promise<StatusResponse>;
  // 課題のツリー（表示の区分・タスクの進み・費用・未のフォローアップの件数は core の値）。
  tree(name: string): Promise<TreeResponse>;
  // フォローアップ。status の既定は open。
  followups(name: string, status?: string): Promise<FollowupListResponse>;
  challenges(name: string): Promise<ListCardResponse>;
  // サイクルの様子。対象のサイクルのスーパーバイザーの手と費用の内訳（supervisor_actions・costs）を含む。
  cycle(name: string): Promise<CycleScreen>;
  challenge(name: string, id: string): Promise<ShowCardResponse>;
  log(name: string, id: string): Promise<LogResponse>;
  runReport(name: string, runId: string): Promise<RunReportResponse>;
  needsHuman(): Promise<NeedsHumanResponse>;
  questions(): Promise<QuestionsResponse>;
  // 起きたことの知らせ（新しい順に、直近の 20 件まで）。
  notices(name: string): Promise<NoticesResponse>;
}

// 承認の応答は、承認後の課題（ShowCardResponse）に、サイクルを始めたかの結果 cycle を足した形。
// tree は、承認が確定した後の課題のツリーの読み直しに失敗したとき null（応答は成功のまま。ツリーは次の読み直しで出す）。
export type ApproveResponse = Omit<ShowCardResponse, "tree"> & {
  tree: ShowCardResponse["tree"] | null;
  cycle: CycleStart;
};

// 承認（計画の承認・完了の承認。2 段階）。① 要約と版を得る → ② 版を添えて承認する。
export interface ApprovalApi {
  approvalSummary(name: string, id: string): Promise<ApprovalSummaryResponse>;
  approve(name: string, id: string, version: number): Promise<ApproveResponse>;
}

// 「サイクルを回す」のボタンが呼ぶサイクルの起動。成功は {"started": true}。
export interface CycleApi {
  startCycle(name: string): Promise<CycleStarted>;
}

// 対話の欄。読み取りは GET …/chat・会話の一覧・全文、書き込みは文を送る・止める・新しい会話・確認への答え・
// 閉じた会話の続き（CSRF つきの POST）。
export interface ChatApi {
  chat(name: string): Promise<ChatResponse>;
  chatSend(name: string, req: ChatPostRequest): Promise<ChatPostResponse>;
  chatStop(name: string): Promise<unknown>;
  chatNew(name: string): Promise<unknown>;
  // 許可の確認と問いへの答え。
  chatAnswer(
    name: string,
    promptId: number,
    req: ChatPromptAnswerRequest,
  ): Promise<unknown>;
  chatSessions(name: string): Promise<ChatSessionsResponse>;
  chatResume(name: string, sessionId: string): Promise<unknown>;
  chatTranscript(
    name: string,
    sessionId: string,
  ): Promise<ChatTranscriptResponse>;
}

// 画面のボタンで受ける操作（いまの守りの CSRF つきの POST）。回答は保留の ID で指し、読んだときの照合の値を送る。
export interface ScreenWriteApi {
  answerHold(
    name: string,
    challengeId: string,
    holdId: string,
    req: HoldAnswerRequest,
  ): Promise<HoldAnswerResponse>;
  ingest(name: string, req: IngestRequest): Promise<IngestAcceptedResponse>;
  cycleStop(name: string): Promise<CycleStopResponse>;
  followupsDone(
    name: string,
    req: FollowupDoneRequest,
  ): Promise<FollowupListResponse>;
  followupsDrop(
    name: string,
    req: FollowupDropRequest,
  ): Promise<FollowupListResponse>;
}

export type ApiClient = ReadApi &
  ApprovalApi &
  CycleApi &
  ChatApi &
  ScreenWriteApi;

const readApi: ReadApi = {
  workspaces: () => get<WorkspaceList>("/api/v1/workspaces"),
  status: (name) => get<StatusResponse>(`${ws(name)}/status`),
  statusIngestable: (name) =>
    get<StatusResponse>(`${ws(name)}/status?ingestable=true`),
  tree: (name) => get<TreeResponse>(`${ws(name)}/tree`),
  followups: (name, status) =>
    get<FollowupListResponse>(
      `${ws(name)}/followups${status === undefined ? "" : `?status=${encodeURIComponent(status)}`}`,
    ),
  challenges: (name) => get<ListCardResponse>(`${ws(name)}/challenges`),
  cycle: (name) => get<CycleScreen>(`${ws(name)}/cycle`),
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
  notices: (name) => get<NoticesResponse>(`${ws(name)}/notices`),
};

const approvalApi: ApprovalApi = {
  approvalSummary: (name, id) =>
    get<ApprovalSummaryResponse>(
      `${ws(name)}/challenges/${encodeURIComponent(id)}/approval`,
    ),
  approve: (name, id, version) =>
    post<ApproveResponse>(
      `${ws(name)}/challenges/${encodeURIComponent(id)}/approve`,
      { version },
    ),
};

const cycleApi: CycleApi = {
  startCycle: (name) => post<CycleStarted>(`${ws(name)}/cycle`),
};

const chatSessionPath = (name: string, sessionId: string) =>
  `${ws(name)}/chat/sessions/${encodeURIComponent(sessionId)}`;

// 開発時の mock モードの、画面の通しの試験が本物の要求を確かめるときにも使う（mock/install.ts）。
export const chatApi: ChatApi = {
  chat: (name) => get<ChatResponse>(`${ws(name)}/chat`),
  chatSend: (name, req) =>
    post<ChatPostResponse>(`${ws(name)}/chat/messages`, req),
  chatStop: (name) => post<unknown>(`${ws(name)}/chat/stop`),
  chatNew: (name) => post<unknown>(`${ws(name)}/chat/new`),
  chatAnswer: (name, promptId, req) =>
    post<unknown>(`${ws(name)}/chat/prompts/${promptId}/answer`, req),
  chatSessions: (name) =>
    get<ChatSessionsResponse>(`${ws(name)}/chat/sessions`),
  chatResume: (name, sessionId) =>
    post<unknown>(`${chatSessionPath(name, sessionId)}/resume`),
  chatTranscript: (name, sessionId) =>
    get<ChatTranscriptResponse>(
      `${chatSessionPath(name, sessionId)}/transcript`,
    ),
};

const screenWriteApi: ScreenWriteApi = {
  answerHold: (name, challengeId, holdId, req) =>
    post<HoldAnswerResponse>(
      `${ws(name)}/challenges/${encodeURIComponent(challengeId)}/holds/${encodeURIComponent(holdId)}/answer`,
      req,
    ),
  ingest: (name, req) =>
    post<IngestAcceptedResponse>(`${ws(name)}/ingest`, req),
  cycleStop: (name) => post<CycleStopResponse>(`${ws(name)}/cycle/stop`),
  followupsDone: (name, req) =>
    post<FollowupListResponse>(`${ws(name)}/followups/done`, req),
  followupsDrop: (name, req) =>
    post<FollowupListResponse>(`${ws(name)}/followups/drop`, req),
};

const realApi: ApiClient = {
  ...readApi,
  ...approvalApi,
  ...cycleApi,
  ...chatApi,
  ...screenWriteApi,
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
  statusIngestable: (n) => impl.statusIngestable(n),
  tree: (n) => impl.tree(n),
  followups: (n, s) => impl.followups(n, s),
  challenges: (n) => impl.challenges(n),
  cycle: (n) => impl.cycle(n),
  challenge: (n, id) => impl.challenge(n, id),
  log: (n, id) => impl.log(n, id),
  runReport: (n, r) => impl.runReport(n, r),
  needsHuman: () => impl.needsHuman(),
  questions: () => impl.questions(),
  notices: (n) => impl.notices(n),
  approvalSummary: (n, id) => impl.approvalSummary(n, id),
  approve: (n, id, v) => impl.approve(n, id, v),
  startCycle: (n) => impl.startCycle(n),
  chat: (n) => impl.chat(n),
  chatSend: (n, r) => impl.chatSend(n, r),
  chatStop: (n) => impl.chatStop(n),
  chatNew: (n) => impl.chatNew(n),
  chatAnswer: (n, p, r) => impl.chatAnswer(n, p, r),
  chatSessions: (n) => impl.chatSessions(n),
  chatResume: (n, s) => impl.chatResume(n, s),
  chatTranscript: (n, s) => impl.chatTranscript(n, s),
  answerHold: (n, c, h, r) => impl.answerHold(n, c, h, r),
  ingest: (n, r) => impl.ingest(n, r),
  cycleStop: (n) => impl.cycleStop(n),
  followupsDone: (n, r) => impl.followupsDone(n, r),
  followupsDrop: (n, r) => impl.followupsDrop(n, r),
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
