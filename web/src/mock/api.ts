// ブラウザ内の偽の API。ApiClient と同じ形を MockStore の上に実装する。
// 失敗は本物と同じエラーコード（ApiError）で返し、画面の失敗表示を確かめられるようにする。

import type { ApiClient } from "../api/client.ts";
import type {
  LogResponse,
  NeedsHumanResponse,
  QuestionsResponse,
  RunReportResponse,
  ShowCardResponse,
  WorkspaceList,
} from "../api/types.gen.ts";
import {
  ApiError,
  type ApprovalItem,
  type ApprovalPrepared,
  type ApprovalTarget,
  type DecisionPrepared,
  type DecisionRequest,
  PRIORITIES,
  URGENCIES,
} from "../api/write.ts";
import {
  type MockChallenge,
  type MockStore,
  type MockWorkspace,
  STATUS_LABEL,
  cardOf,
  statusOf,
  toCard,
} from "./store.ts";

const EXPIRY_MS = 5 * 60_000;

interface Session {
  id: string;
  ws: string;
  expires: number;
  used: boolean;
}
interface ApprovalSession extends Session {
  items: ApprovalItem[];
}
interface DecisionSession extends Session {
  req: DecisionRequest;
  version: number;
}

export interface MockOptions {
  latencyMs?: number;
}

export function createMockApi(
  store: MockStore,
  opts: MockOptions = {},
): ApiClient {
  const latency = opts.latencyMs ?? 200;
  const sessions = new Map<string, ApprovalSession | DecisionSession>();
  let seq = 0;

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
  const touch = (m: MockChallenge) => {
    m.c.version += 1;
    m.c.updated_at = store.now();
  };
  const setStatus = (m: MockChallenge, status: string) => {
    m.c.status = status;
    m.c.status_label = STATUS_LABEL[status] ?? status;
  };
  const checkVersion = (m: MockChallenge, version: number) => {
    if (m.c.version !== version)
      throw new ApiError(
        "conflict",
        `${m.c.id} の版が ${version} から ${m.c.version} に変わっています`,
        409,
      );
  };
  const validate = (cond: boolean, message: string) => {
    if (!cond) throw new ApiError("validation_failed", message, 400);
  };
  const newSession = (ws: string): Session => ({
    id: `S-${++seq}`,
    ws,
    expires: Date.now() + EXPIRY_MS,
    used: false,
  });
  const takeSession = <T extends Session>(ws: string, id: string): T => {
    const s = sessions.get(id) as T | undefined;
    if (!s || s.ws !== ws)
      throw new ApiError(
        "verification_rejected",
        "署名の対象がありません",
        403,
      );
    if (s.used)
      throw new ApiError(
        "verification_rejected",
        "この署名は使用済みです",
        403,
      );
    if (Date.now() > s.expires)
      throw new ApiError(
        "verification_rejected",
        "署名の対象が失効しました",
        403,
      );
    s.used = true;
    return s;
  };

  const validateTitle = (t: string) => {
    validate(t.trim() !== "", "タイトルは必須です");
    validate(t.length <= 200, "タイトルは 200 字までです");
  };
  const validateUrgency = (u: string | null) =>
    validate(
      u === null || (URGENCIES as readonly string[]).includes(u),
      "緊急度は 高・中・低 のどれかです",
    );

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

    async cardExtra(name, id) {
      await wait();
      return itemOf(name, id).extra;
    },
    async cycleInfo(name) {
      await wait();
      return wsOf(name).cycle;
    },

    async createChallenge(name, input) {
      await wait();
      const w = wsOf(name);
      validateTitle(input.title);
      validateUrgency(input.urgency);
      const id = `C-${w.next++}`;
      const now = store.now();
      const m: MockChallenge = {
        c: {
          created_at: now,
          description: input.description,
          done_criteria: input.done_criteria,
          id,
          priority: null,
          reporter: "masami",
          status: "unclassified",
          status_label: STATUS_LABEL.unclassified ?? "未分類",
          title: input.title,
          updated_at: now,
          urgency: input.urgency,
          version: 1,
        },
        headline: null,
        context: null,
        holds: [],
        operations: [],
        plans: [],
        approvals: [],
        runs: [],
        flags: {},
        extra: { expected: null, actual: null, not_doing: [], diagram: null },
      };
      w.items.push(m);
      store.emit(name);
      return { challenge: m.c };
    },
    async editChallenge(name, id, input) {
      await wait();
      const m = itemOf(name, id);
      validateTitle(input.title);
      validateUrgency(input.urgency);
      checkVersion(m, input.version);
      m.c.title = input.title;
      m.c.description = input.description;
      m.c.done_criteria = input.done_criteria;
      m.c.urgency = input.urgency;
      if (input.headline !== undefined) m.headline = input.headline;
      if (input.context !== undefined) m.context = input.context;
      touch(m);
      store.emit(name);
      return { challenge: m.c };
    },
    async prioritize(name, id, input) {
      await wait();
      const m = itemOf(name, id);
      validate(
        (PRIORITIES as readonly string[]).includes(input.priority),
        "優先度は P0・P1・P2 のどれかです",
      );
      validate(
        m.c.status !== "unclassified" && m.c.status !== "done",
        "優先度を変えられるのは、分類済み以降で完了していない課題だけです",
      );
      checkVersion(m, input.version);
      m.c.priority = input.priority;
      touch(m);
      store.emit(name);
      return { challenge: m.c };
    },

    async prepareApproval(
      name,
      targets: ApprovalTarget[],
    ): Promise<ApprovalPrepared> {
      await wait();
      const w = wsOf(name);
      validate(targets.length >= 1, "承認の対象を 1 件以上選んでください");
      const seen = new Set<string>();
      for (const t of targets) {
        const key = `${t.kind}:${t.id}`;
        validate(!seen.has(key), `${t.id} が重複しています`);
        seen.add(key);
      }
      const completionIds = new Set(
        targets
          .filter((t) => t.kind === "challenge")
          .filter(
            (t) =>
              w.items.find((i) => i.c.id === t.id)?.c.status ===
              "awaiting_completion_approval",
          )
          .map((t) => t.id),
      );
      const items: ApprovalItem[] = targets.map((t) => {
        if (t.kind === "challenge") {
          const m = itemOf(name, t.id);
          const s = m.c.status;
          validate(
            s === "awaiting_plan_approval" ||
              s === "awaiting_completion_approval",
            `${t.id} は承認待ちではありません`,
          );
          const completion = s === "awaiting_completion_approval";
          const sim = completion
            ? m.operations
                .filter((o) => o.kind === "release" && o.state === "pending")
                .map((o) => o.id)
            : [];
          return {
            kind: "challenge",
            id: t.id,
            title: m.c.title,
            approval_kind: completion ? "completion" : "plan",
            version: m.c.version,
            simultaneous_operation_ids: sim,
            hold_release: false,
            summary: completion
              ? `完了条件: ${m.c.done_criteria || "（未記入）"}`
              : `計画 v${m.plans.at(-1)?.version ?? "-"}: ${m.plans.at(-1)?.body ?? "（計画なし）"}`,
            pr_head: completion ? "9f2c1ab（モックの値）" : null,
          };
        }
        const owner = w.items.find((i) =>
          i.operations.some((o) => o.id === t.id),
        );
        const o = owner?.operations.find((x) => x.id === t.id);
        validate(!!owner && !!o, `不可逆操作 ${t.id} がありません`);
        if (!owner || !o) throw new Error("unreachable");
        validate(o.state === "pending", `${t.id} は承認待ちではありません`);
        validate(
          !(o.kind === "release" && completionIds.has(owner.c.id)),
          `${t.id} は ${owner.c.id} の完了の承認で同時に承認されるため、独立して選べません`,
        );
        return {
          kind: "operation",
          id: t.id,
          title: `${o.kind}: ${owner.c.title}`,
          approval_kind: "operation",
          version: o.version,
          simultaneous_operation_ids: [],
          hold_release: false,
          summary: o.summary,
          pr_head: null,
        };
      });
      const s: ApprovalSession = { ...newSession(name), items };
      sessions.set(s.id, s);
      return {
        challenge_id: s.id,
        expires_at: new Date(s.expires).toISOString(),
        items,
      };
    },
    async executeApproval(name, input) {
      await wait();
      const w = wsOf(name);
      const s = takeSession<ApprovalSession>(name, input.challenge_id);
      // 集合が確認画面の時点から変わっていないこと（署名に束ねた正規形の照合の代わり）
      const a = s.items.map((i) => `${i.id}:${i.version}`).join("\n");
      const b = input.items.map((i) => `${i.id}:${i.version}`).join("\n");
      if (a !== b)
        throw new ApiError(
          "verification_rejected",
          "署名の対象と実行を求められた集合が一致しません",
          403,
        );
      // 1 件でも版が変わっていれば全体を拒否する（一部だけは承認しない）
      const changed: string[] = [];
      for (const i of s.items) {
        const cur =
          i.kind === "challenge"
            ? w.items.find((x) => x.c.id === i.id)?.c.version
            : w.items.flatMap((x) => x.operations).find((o) => o.id === i.id)
                ?.version;
        if (cur !== i.version) changed.push(i.id);
      }
      if (changed.length > 0)
        throw new ApiError(
          "conflict",
          `版が変わっています: ${changed.join("、")}（何も承認していません）`,
          409,
        );
      for (const i of s.items) {
        if (i.kind === "challenge") {
          const m = itemOf(name, i.id);
          m.approvals.push({
            actor: "masami",
            channel: "ui",
            decided_at: store.now(),
            decision: "approved",
            kind: i.approval_kind,
            operation_id: null,
            reason: null,
            target_version: i.version,
            verification: "passkey",
          });
          if (i.approval_kind === "plan") setStatus(m, "in_progress");
          else {
            setStatus(m, "done");
            for (const o of m.operations)
              if (i.simultaneous_operation_ids.includes(o.id)) {
                o.state = "approved";
                o.version += 1;
              }
          }
          touch(m);
        } else {
          for (const m of w.items)
            for (const o of m.operations)
              if (o.id === i.id) {
                o.state = "approved";
                o.version += 1;
                touch(m);
              }
        }
      }
      store.emit(name);
      return { approved: s.items.map((i) => i.id) };
    },

    async prepareDecision(name, req): Promise<DecisionPrepared> {
      await wait();
      const m = itemOf(name, req.challenge_id);
      let summary: string;
      if (req.action === "reject") {
        validate(
          m.c.status === "awaiting_plan_approval" ||
            m.c.status === "awaiting_completion_approval",
          "差し戻せるのは承認待ちの課題だけです",
        );
        validate(req.reason.trim() !== "", "理由は必須です");
        summary = `差し戻し。理由: ${req.reason}`;
      } else if (req.action === "answer") {
        validate(
          m.c.status === "awaiting_human" &&
            m.holds.some((h) => h.answer === null),
          "回答できるのは未回答の問いがある課題だけです",
        );
        validate(req.answer.trim() !== "", "回答は必須です");
        summary = `回答: ${req.answer}`;
      } else {
        validate(!!m.flags.budget, "予算を使い切った課題だけが増額できます");
        validate(
          Number.isFinite(req.amount_usd) &&
            req.amount_usd > 0 &&
            req.amount_usd <= 100,
          "額は 0 より大きく 100 ドル以下にしてください",
        );
        summary = `予算を $${req.amount_usd.toFixed(2)} 増額`;
      }
      const s: DecisionSession = {
        ...newSession(name),
        req,
        version: m.c.version,
      };
      sessions.set(s.id, s);
      return {
        sign_id: s.id,
        expires_at: new Date(s.expires).toISOString(),
        challenge_id: m.c.id,
        title: m.c.title,
        version: m.c.version,
        action: req.action,
        summary,
      };
    },
    async executeDecision(name, input) {
      await wait();
      const s = takeSession<DecisionSession>(name, input.sign_id);
      const m = itemOf(name, s.req.challenge_id);
      checkVersion(m, s.version);
      const req = s.req;
      if (req.action === "reject") {
        const completion = m.c.status === "awaiting_completion_approval";
        m.approvals.push({
          actor: "masami",
          channel: "ui",
          decided_at: store.now(),
          decision: "rejected",
          kind: completion ? "completion" : "plan",
          operation_id: null,
          reason: req.reason,
          target_version: m.c.version,
          verification: "passkey",
        });
        setStatus(m, completion ? "in_progress" : "classified");
      } else if (req.action === "answer") {
        const h = m.holds.find((x) => x.answer === null);
        if (h) {
          h.answer = req.answer;
          h.answered_at = store.now();
          h.answered_by = "masami";
          setStatus(m, h.from_status);
        }
      } else {
        m.flags.budget = undefined;
      }
      touch(m);
      store.emit(s.ws);
      return { action: req.action, challenge_id: m.c.id };
    },
  };
  return api;
}
