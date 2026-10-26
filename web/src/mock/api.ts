// ブラウザ内の偽の API。ApiClient と同じ形を MockStore の上に実装する（読み取りだけ）。
// 失敗は本物と同じエラーコード（ApiError）で返し、画面の失敗表示を確かめられるようにする。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/proposed.ts";
import type {
  ApprovalSummaryResponse,
  CycleStarted,
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
  STATUS_LABEL,
  cardOf,
  pairsOf,
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
        challenge: {
          ...m.c,
          dependencies: [],
          dependents: [],
          followup_of: null,
          improvement: null,
        },
        holds: m.holds,
        operations: m.operations,
        plans: m.plans,
        runs: m.runs,
        pairs: pairsOf(m.stages),
        source_binding: null,
        stages: m.stages,
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

    // ① 要約と版。完了確認待ちなら release の PR の head も付ける（取得できない release も 1 件混ぜて表示を確かめる）。
    async approvalSummary(name, id): Promise<ApprovalSummaryResponse> {
      await wait();
      const m = itemOf(name, id);
      const plan = m.plans.at(-1) ?? null;
      let kind: string;
      if (m.c.status === "awaiting_plan_approval") kind = "plan";
      else if (m.c.status === "awaiting_completion_approval")
        kind = "completion";
      else
        throw new ApiError(
          "invalid_transition",
          `課題 ${id} は承認待ちではありません`,
          409,
        );
      const releases = m.operations.filter(
        (o) => o.kind === "release" && o.state === "pending",
      );
      return {
        approval: {
          challenge_id: id,
          done_criteria: m.c.done_criteria === "" ? null : m.c.done_criteria,
          kind,
          other_pending_operations: m.operations
            .filter((o) => o.state === "pending" && o.kind !== "release")
            .map((o) => ({ id: o.id, kind: o.kind, summary: o.summary })),
          pairs: pairsOf(m.stages),
          plan:
            kind === "plan" && plan
              ? { body: plan.body, version: plan.version }
              : null,
          release_held: kind === "completion",
          releases:
            kind === "completion"
              ? releases.map((o, i) => ({
                  head_sha: i === 0 ? "3f9c2ab7d41e" : null,
                  id: o.id,
                  ref: o.ref ?? "refs/pull/412/head",
                  summary: o.summary,
                }))
              : [],
          stages: m.stages,
          title: m.c.title,
          version: m.c.version,
        },
      };
    },
    async startCycle(): Promise<CycleStarted> {
      await wait();
      return { started: true };
    },
    // ② 版が違えば conflict。通れば状態を進める（ブラウザ内だけの偽の書き込み）。
    async approve(name, id, version): Promise<ShowCardResponse> {
      await wait();
      const m = itemOf(name, id);
      if (m.c.version !== version)
        throw new ApiError("conflict", "版が違う", 409);
      const completion = m.c.status === "awaiting_completion_approval";
      if (!completion && m.c.status !== "awaiting_plan_approval")
        throw new ApiError("invalid_transition", "承認待ちではない", 409);
      m.c.status = completion ? "done" : "in_progress";
      m.c.status_label = STATUS_LABEL[m.c.status] ?? m.c.status;
      m.c.version += 1;
      return {
        approvals: m.approvals,
        card: cardOf(m),
        challenge: {
          ...m.c,
          dependencies: [],
          dependents: [],
          followup_of: null,
          improvement: null,
        },
        holds: m.holds,
        operations: m.operations,
        plans: m.plans,
        runs: m.runs,
        pairs: pairsOf(m.stages),
        source_binding: null,
        stages: m.stages,
      };
    },
    async cycle(name) {
      await wait();
      return wsOf(name).cycle;
    },
    async challengeGraph(name) {
      await wait();
      return wsOf(name).graph;
    },
    async agentProfiles() {
      await wait();
      return {
        agents: Object.values(store.workspaces).map((w) => ({
          name: w.meta.name,
          icon: w.icon,
        })),
      };
    },
  };
  return api;
}
