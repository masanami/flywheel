// ブラウザ内の偽の API。ApiClient と同じ形を MockStore の上に実装する（読み取りだけ）。
// 失敗は本物と同じエラーコード（ApiError）で返し、画面の失敗表示を確かめられるようにする。

import type { ApiClient, ApproveResponse } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import { CONDITIONS_AFTER_RELEASE_HEADINGS } from "../api/types.gen.ts";
import type {
  ApprovalSummaryResponse,
  ChatMessage,
  ChatResponse,
  CycleStarted,
  FollowupListResponse,
  LogResponse,
  NeedsHumanResponse,
  QuestionsResponse,
  RunReportResponse,
  ShowCardResponse,
  TreeChallenge,
  TreeResponse,
  WorkspaceList,
} from "../api/types.gen.ts";
import { HOLD_KIND_PARENT_DIRECT } from "../labels.ts";
import { ALPHA_FOLLOWUPS } from "./followups.ts";
import { alphaLogExtras } from "./log.ts";
import { RICH_PLANS } from "./plan.ts";
import {
  type MockChallenge,
  type MockStore,
  type MockWorkspace,
  STATUS_LABEL,
  cardOf,
  ingestableOf,
  pairsOf,
  statusOf,
  toCard,
  waitingOf,
} from "./store.ts";
import { alphaDetailExtras, alphaTree } from "./tree.ts";

/** 課題の詳細の tree の空の値（試作の画面が使うまでは、タスクも件数も持たない）。 */
function emptyTree(
  id: string,
  title: string,
  status: string,
  statusLabel: string,
): TreeChallenge {
  return {
    budget_usd: null,
    children: [],
    cost_usd: 0,
    dependencies: [],
    followups_open: 0,
    id,
    next_human_actions: [],
    parent: null,
    progress: null,
    status,
    status_label: statusLabel,
    tasks: [],
    tasks_done: 0,
    tasks_total: 0,
    title,
    updated_at: "2026-11-01T00:00:00.000Z",
    uses_integration: null,
  };
}

// 偽のサーバが返す見出し。正本は internal/view の ConditionsAfterReleaseHeading で、生成物
// （types.gen.ts）の並びは Go の ConditionsOutlook の値の順（本番反映を承認しない／承認する／承認済みで未実行）。
// UI の承認は本番反映を承認しない側。
const [
  CONDITIONS_AFTER_RELEASE_HEADING,
  ,
  CONDITIONS_AFTER_APPROVED_RELEASE_HEADING,
] = CONDITIONS_AFTER_RELEASE_HEADINGS;

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

  // 課題の詳細の応答。alpha は課題のツリーの偽の応答（alphaTree）の値をそのまま tree に使い、
  // 画面が core の値を出すだけであることを確かめられるようにする。それ以外のエージェントは空の tree。
  const showOf = (
    name: string,
    id: string,
    m: MockChallenge,
  ): ShowCardResponse => {
    const t =
      name === "alpha"
        ? [...alphaTree().challenges, ...alphaTree().recent_done].find(
            (x) => x.id === id,
          )
        : undefined;
    const extra = name === "alpha" ? alphaDetailExtras(id) : {};
    return {
      approvals: m.approvals,
      card: cardOf(m),
      challenge: {
        ...m.c,
        dependencies: [],
        uses_integration: t?.uses_integration ?? null,
        dependents: extra.dependents ?? [],
        followup_of: null,
        improvement: null,
        integration_ref: null,
      },
      conflict_fixes: extra.conflict_fixes ?? [],
      direct_completions: m.directCompletions,
      followups: extra.followups ?? [],
      holds: m.holds,
      operations: m.operations,
      plans: m.plans,
      runs: m.runs.map((r) => ({
        ...r,
        fix_pending_ref: extra.fix_pending_runs?.includes(r.id)
          ? "abc1234"
          : null,
      })),
      pairs: pairsOf(m.stages),
      source_binding: extra.source_binding ?? null,
      source_children: extra.source_children ?? [],
      supervisor_actions: [],
      supervisor_hands_taken: {},
      stages: m.stages,
      tasks: extra.tasks ?? [],
      tree: t ?? emptyTree(m.c.id, m.c.title, m.c.status, m.c.status_label),
      waiting: waitingOf(wsOf(name), id),
    };
  };

  // 対話の欄の偽の会話（エージェントごと。ブラウザ内だけ）。送ると、すぐ返事が終わる。
  const chats: Record<string, { messages: ChatMessage[]; cost: number }> = {};
  const chatOf = (name: string) => {
    wsOf(name);
    chats[name] ??= { messages: [], cost: 0 };
    return chats[name];
  };

  const api: ApiClient = {
    async chat(name): Promise<ChatResponse> {
      await wait();
      const c = chatOf(name);
      return {
        cost_usd: c.cost,
        messages: c.messages,
        session_id: c.messages.length > 0 ? `mock-${name}` : null,
        started_at: c.messages[0]?.created_at ?? null,
        state: "idle",
        tools: "restricted",
      };
    },
    async chatSend(name, req) {
      await wait();
      const c = chatOf(name);
      const now = new Date().toISOString();
      const id = c.messages.length + 1;
      c.messages.push(
        {
          activity: [],
          body: req.text,
          cost_usd: 0,
          created_at: now,
          id,
          role: "owner",
          state: "done",
          viewing: null,
        },
        {
          activity: [{ command: "flywheel status", ok: true }],
          body: "# 確認しました\n\n- 状況を見ました",
          cost_usd: 0.12,
          created_at: now,
          id: id + 1,
          role: "orchestrator",
          state: "done",
          viewing: null,
        },
      );
      c.cost += 0.12;
      return { id };
    },
    async chatStop(name) {
      await wait();
      chatOf(name);
      return {};
    },
    async chatNew(name) {
      await wait();
      chats[name] = { messages: [], cost: 0 };
      return {};
    },
    async workspaces(): Promise<WorkspaceList> {
      await wait();
      return { workspaces: Object.values(store.workspaces).map((w) => w.meta) };
    },
    async status(name) {
      await wait();
      return statusOf(wsOf(name));
    },
    async statusIngestable(name) {
      await wait();
      return {
        ...statusOf(wsOf(name)),
        ingestable: ingestableOf(name),
      };
    },
    async tree(name): Promise<TreeResponse> {
      await wait();
      if (name === "alpha") {
        wsOf(name);
        return alphaTree();
      }
      const all = wsOf(name).items.map((m) =>
        emptyTree(
          m.c.id,
          m.c.title,
          m.c.status,
          STATUS_LABEL[m.c.status] ?? m.c.status,
        ),
      );
      return {
        challenges: all.filter((t) => t.status !== "done"),
        recent_done: all.filter((t) => t.status === "done"),
      };
    },
    async followups(name, status): Promise<FollowupListResponse> {
      await wait();
      wsOf(name);
      const all = name === "alpha" ? ALPHA_FOLLOWUPS : [];
      return {
        followups: all.filter(
          (f) => status === "all" || f.status === (status ?? "open"),
        ),
      };
    },
    async challenges(name) {
      await wait();
      return { challenges: wsOf(name).items.map(toCard) };
    },
    async challenge(name, id): Promise<ShowCardResponse> {
      await wait();
      const m = itemOf(name, id);
      return showOf(name, id, m);
    },
    async log(name, id): Promise<LogResponse> {
      await wait();
      const m = itemOf(name, id);
      return {
        activities: [
          ...alphaLogExtras(id),
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
              // 種類が親が直接行うの保留は、オーナーが答える問いではない
              .filter(
                (h) =>
                  h.answer === null &&
                  h.closed_at === null &&
                  h.kind !== HOLD_KIND_PARENT_DIRECT,
              )
              .map((h) => ({
                challenge_id: m.c.id,
                from_status: h.from_status,
                question: h.question,
                raised_at: h.raised_at,
                run_id: null,
                task_id: null,
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
      const hasApprovedRelease = m.operations.some(
        (o) => o.kind === "release" && o.state === "approved",
      );
      // 完了承認画面の値は、本物と同じく要約と 1 回で返す（課題の詳細の偽の応答から、完了確認待ちのときだけ引く）。
      const show = kind === "completion" ? showOf(name, id, m) : null;
      const tasks = (show?.tasks ?? []).flatMap((t) => {
        const node = show?.tree.tasks.find((n) => n.id === t.id);
        if (node !== undefined) {
          return [{ t, phase: node.phase, inTree: true }];
        }
        return t.status === "done" ? [{ t, phase: "done", inTree: false }] : [];
      });
      return {
        approval: {
          challenge_id: id,
          conflict_fixes: show?.conflict_fixes ?? [],
          followups: (show?.followups ?? []).filter((f) => f.status === "open"),
          integration_ref: show?.challenge.integration_ref ?? null,
          tasks: tasks.map(({ t, phase, inTree }) => ({
            budget_usd: t.budget_impl_usd + t.budget_review_usd,
            id: t.id,
            in_tree: inTree,
            phase,
            spent_usd: t.spent_usd,
            title: t.title,
          })),
          uses_integration: show?.challenge.uses_integration ?? null,
          conditions_after_release:
            kind === "completion" ? m.conditionsAfterRelease : [],
          conditions_after_release_heading: hasApprovedRelease
            ? CONDITIONS_AFTER_APPROVED_RELEASE_HEADING
            : CONDITIONS_AFTER_RELEASE_HEADING,
          // 本物と同じく、いまの完了確認待ちが直接行った完了（T17）で入ったときだけ返す
          direct_completion:
            kind === "completion" && m.completionDirect
              ? (m.directCompletions[m.directCompletions.length - 1] ?? null)
              : null,
          done_criteria: m.c.done_criteria === "" ? null : m.c.done_criteria,
          kind,
          mergeability_note: "",
          other_pending_operations: m.operations
            .filter((o) => o.state === "pending" && o.kind !== "release")
            .map((o) => ({ id: o.id, kind: o.kind, summary: o.summary })),
          pairs: pairsOf(m.stages),
          plan:
            kind === "plan" && plan
              ? {
                  body: plan.body,
                  budget_usd: null,
                  summary: null,
                  tasks: [],
                  topics: [],
                  uses_integration: null,
                  ...RICH_PLANS[`${name}/${id}`],
                  version: plan.version,
                }
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
          supervisor_hands_taken: {},
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
    async approve(name, id, version): Promise<ApproveResponse> {
      await wait();
      const m = itemOf(name, id);
      if (m.c.version !== version)
        throw new ApiError("conflict", "版が違う", 409);
      const completion = m.c.status === "awaiting_completion_approval";
      if (!completion && m.c.status !== "awaiting_plan_approval")
        throw new ApiError("invalid_transition", "承認待ちではない", 409);
      m.c.status = completion ? "done" : "in_progress";
      // 完了確認待ちを出る遷移で、直接行った完了の印は消える（本物の challenge.completion_direct と同じ）
      m.completionDirect = false;
      m.c.status_label = STATUS_LABEL[m.c.status] ?? m.c.status;
      m.c.version += 1;
      return {
        ...showOf(name, id, m),
        cycle: { reason: "started", started: true },
      };
    },
    async cycle(name) {
      await wait();
      return wsOf(name).cycle;
    },
  };
  return api;
}
