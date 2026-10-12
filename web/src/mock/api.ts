// ブラウザ内の偽の API。ApiClient と同じ形を MockStore の上に実装する（読み取りだけ）。
// 失敗は本物と同じエラーコード（ApiError）で返し、画面の失敗表示を確かめられるようにする。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/proposed.ts";
import type {
  LogResponse,
  NeedsHumanResponse,
  QuestionsResponse,
  RunReportResponse,
  ShowCardResponse,
  WorkspaceList,
} from "../api/types.gen.ts";
import {
  type MockChallenge,
  type MockStore,
  type MockWorkspace,
  cardOf,
  statusOf,
  toCard,
} from "./store.ts";

export interface MockOptions {
  latencyMs?: number;
}

export function createMockApi(
  store: MockStore,
  opts: MockOptions = {},
): ApiClient {
  const latency = opts.latencyMs ?? 200;

  const wait = () =>
    latency > 0 ? new Promise<void>((r) => setTimeout(r, latency)) : undefined;

  const wsOf = (name: string): MockWorkspace => {
    const w = store.workspaces[name];
    if (!w)
      throw new ApiError(
        "not_found",
        `ワークスペース ${name} がありません`,
        404,
      );
    if (w.meta.state !== "ok")
      throw new ApiError(w.meta.state, w.meta.error ?? w.meta.state, 404);
    return w;
  };
  const itemOf = (name: string, id: string): MockChallenge => {
    const m = wsOf(name).items.find((i) => i.c.id === id);
    if (!m) throw new ApiError("not_found", `課題 ${id} がありません`, 404);
    return m;
  };

  const api: ApiClient = {
    async workspaces(): Promise<WorkspaceList> {
      await wait();
      return { workspaces: Object.values(store.workspaces).map((w) => w.meta) };
    },
    async status(name) {
      await wait();
      return statusOf(wsOf(name));
    },
    async challenges(name) {
      await wait();
      return { challenges: wsOf(name).items.map(toCard) };
    },
    async challenge(name, id): Promise<ShowCardResponse> {
      await wait();
      const m = itemOf(name, id);
      return {
        approvals: m.approvals,
        card: cardOf(m),
        challenge: m.c,
        holds: m.holds,
        operations: m.operations,
        plans: m.plans,
        runs: m.runs,
        source_binding: null,
      };
    },
    async log(name, id): Promise<LogResponse> {
      await wait();
      const m = itemOf(name, id);
      return {
        activities: [
          {
            action: "create",
            actor: m.c.reporter,
            after: null,
            at: m.c.created_at,
            before: null,
            channel: "cli",
            entity: "challenge",
            entity_id: id,
            verification: "none",
          },
        ],
      };
    },
    async runReport(name, runId): Promise<RunReportResponse> {
      await wait();
      wsOf(name);
      return {
        report: {
          assumptions: ["モックの仮定: 既存のテストは変えない"],
          run_id: runId,
          unverified: ["モックの未検証: 本番の負荷での挙動"],
        },
      };
    },
    async needsHuman(): Promise<NeedsHumanResponse> {
      await wait();
      const okWs = Object.values(store.workspaces).filter(
        (w) => w.meta.state === "ok",
      );
      return {
        unreadable_workspaces: [],
        workspaces: okWs.map((w) => ({
          name: w.meta.name,
          needs_human: statusOf(w).needs_human,
        })),
      };
    },
    async questions(): Promise<QuestionsResponse> {
      await wait();
      const qs = Object.values(store.workspaces)
        .filter((w) => w.meta.state === "ok")
        .flatMap((w) =>
          w.items.flatMap((m) =>
            m.holds
              .filter((h) => h.answer === null)
              .map((h) => ({
                challenge_id: m.c.id,
                from_status: h.from_status,
                question: h.question,
                raised_at: h.raised_at,
                run_id: null,
                title: m.c.title,
                workspace: w.meta.name,
              })),
          ),
        );
      qs.sort((a, b) => (a.raised_at < b.raised_at ? -1 : 1));
      return { questions: qs, unreadable_workspaces: [] };
    },

    async cycleInfo(name) {
      await wait();
      return wsOf(name).cycle;
    },
  };
  return api;
}
