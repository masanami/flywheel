// ブラウザ内の偽のストア。開発時の mock モードだけが読み込む（本番のビルドには入らない）。
// 返す形は types.gen.ts と仕様の API の節に従う。状態の導出は本物では core がやるが、ここでは同じ表を写す。

import type { ChallengeGraphResponse } from "../api/proposed.ts";
import type {
  Approval,
  BudgetExhausted,
  Card,
  Challenge,
  ChallengeCard,
  CycleStatus,
  Hold,
  Operation,
  PlanDetail,
  Run,
  StagePair,
  Stages,
  StatusResponse,
  Workspace,
} from "../api/types.gen.ts";
import type { DiscrepancyKind } from "../labels.ts";
import { seedGraphs } from "./graph.ts";

export interface Flags {
  running?: boolean;
  unresponsive?: boolean;
  waitingCi?: { checks: string; pr_url: string };
  budget?: BudgetExhausted;
  discrepancy?: DiscrepancyKind[];
  triage?: { reason: string; run_id: string };
  slot?: { path: string; repo: string; slot_id: string; run_id: string };
}

// 段階の欄のサンプル（無ければ全部 null）。本物では core が持つ値を写す。
export const NO_STAGES: Stages = {
  actual: null,
  expected: null,
  figure: null,
  issues: null,
  issues_answer: null,
  non_goals: null,
};

// 予想と実物を位置で対にする（本物の core.Pairs と同じ。偽の API だけで使う）。
export function pairsOf(s: Stages): StagePair[] {
  const n = Math.max(s.expected?.length ?? 0, s.actual?.length ?? 0);
  return Array.from({ length: n }, (_, i) => ({
    expected: s.expected?.[i] ?? null,
    actual: s.actual?.[i] ?? null,
  }));
}

export interface MockChallenge {
  stages: Stages;
  c: Challenge;
  headline: string | null;
  context: string | null;
  holds: Hold[];
  operations: Operation[];
  plans: PlanDetail[];
  approvals: Approval[];
  runs: Run[];
  flags: Flags;
}

export interface MockWorkspace {
  meta: Workspace;
  items: MockChallenge[];
  cycle: CycleStatus;
  graph: ChallengeGraphResponse;
  // 見た目の設定の案（proposed.ts の AgentProfile）。絵文字か画像の URL、無ければ null
  icon: string | null;
}

export const STATUS_LABEL: Record<string, string> = {
  unclassified: "未分類",
  classified: "分類済",
  awaiting_plan_approval: "計画承認待ち",
  in_progress: "着手中",
  verifying: "検証中",
  awaiting_completion_approval: "完了確認待ち",
  done: "完了",
  awaiting_human: "人間対応待ち",
};

const T0 = Date.parse("2026-10-12T09:00:00Z");
const iso = (minAgo: number) => new Date(T0 - minAgo * 60_000).toISOString();

function mk(
  id: string,
  title: string,
  status: string,
  p: Partial<Omit<MockChallenge, "c">> & Partial<Challenge> = {},
): MockChallenge {
  const {
    headline,
    context,
    holds,
    operations,
    plans,
    approvals,
    runs,
    flags,
    stages,
    ...rest
  } = p;
  return {
    stages: stages ?? NO_STAGES,
    c: {
      context: context ?? null,
      created_at: iso(600),
      description: "",
      done_criteria: "",
      headline: headline ?? null,
      id,
      priority: null,
      reporter: "masami",
      status,
      status_label: STATUS_LABEL[status] ?? status,
      title,
      updated_at: iso(30),
      urgency: null,
      version: 1,
      ...rest,
    },
    headline: headline ?? null,
    context: context ?? null,
    holds: holds ?? [],
    operations: operations ?? [],
    plans: plans ?? [],
    approvals: approvals ?? [],
    runs: runs ?? [],
    flags: flags ?? {},
  };
}

const op = (
  id: string,
  challenge_id: string,
  kind: string,
  state: string,
  summary: string,
): Operation => ({
  approved_head: null,
  challenge_id,
  consecutive_failures: 0,
  created_at: iso(120),
  executed_at: null,
  id,
  kind,
  last_execution_result: null,
  merge_commit: null,
  ref: null,
  state,
  summary,
  version: 1,
  waiting_since: null,
});

const plan = (version: number, body: string): PlanDetail => ({
  body,
  created_at: iso(300),
  spec: null,
  version,
});

const run = (id: string, challenge_id: string, p: Partial<Run> = {}): Run => ({
  challenge_id,
  cost_source: "claude",
  cost_usd: 1.25,
  cycle_budget_usd: 20,
  cycle_id: "Y-1",
  ended_at: null,
  id,
  judgment: null,
  kind: "delegation",
  max_budget_usd: 10,
  rate_limited: false,
  result: null,
  session_id: null,
  started_at: iso(45),
  ...p,
});

const LONG = `${"この値はカードに全文を出すと幅 320px の列で何行にも伸びる。".repeat(8)}\n2 行目: 外部の Issue 本文をそのまま取り込んだ想定の長い説明。`;

// 画像の URL を持つアイコンの例（外へ取りに行かないよう、data URL の SVG にする）
const BETA_ICON = `data:image/svg+xml,${encodeURIComponent(
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="8" fill="#2f6fde"/><path d="M8 28 L16 14 L22 24 L26 18 L32 28 Z" fill="#fff"/></svg>',
)}`;

export function seed(): Record<string, MockWorkspace> {
  const graphs = seedGraphs();
  const alpha: MockChallenge[] = [
    mk("C-1", "ログイン画面の文言を直す", "unclassified", {
      description: "ボタンの文言が「送信」のままになっている。",
      urgency: "低",
    }),
    mk("C-2", "検索 API にページングを足す", "classified", {
      priority: "P1",
      urgency: "中",
      description: "一覧が 1000 件を超えると遅い。",
      done_criteria: "page と per_page を受け取り、合計件数を返す。",
      headline: "検索 API をページングで返せるようにする",
      context:
        "全体計画の「一覧の高速化」の最初の段。後の無限スクロールがこの API に乗る。",
    }),
    mk("C-3", "通知メールの再送制御", "awaiting_plan_approval", {
      priority: "P0",
      urgency: "高",
      version: 3,
      description: "同じ通知が 2 通届く不具合。再送の冪等性を保証する。",
      done_criteria: "同一キーの再送は 1 通だけ届く。",
      headline: "通知メールの二重送信を止める",
      context: "障害 INC-42 の再発防止。送信キューの設計が前提になる。",
      stages: {
        ...NO_STAGES,
        issues: [
          {
            question: "再送の重複をどこで止めますか",
            recommended: 1,
            options: [
              {
                label: "送信側で冪等キーを持つ",
                example: "同じキーなら 2 通目は捨てる",
              },
              { label: "受信側で重複を除く", example: null },
            ],
          },
        ],
        issues_answer: "送信側で冪等キーを持つ",
        expected: [
          {
            kind: "behavior",
            scene: "同じ通知を 2 回送る",
            before: "2 通届く",
            after: "1 通だけ届く",
          },
        ],
        non_goals: ["受信側の重複除去は作らない"],
        figure:
          "graph TD\n  A[通知要求] --> B{キーあり?}\n  B -->|はい| C[1 通だけ送る]\n  B -->|いいえ| D[キーを付けて送る]",
      },
      plans: [
        plan(
          1,
          "1. 送信に冪等キーを足す\n2. キューの再試行で同じキーを使う\n3. 重複送信のテストを足す",
        ),
      ],
    }),
    mk("C-4", "管理画面の権限チェックを直す", "awaiting_plan_approval", {
      priority: "P1",
      headline: "閲覧者が編集ボタンを押せてしまう穴をふさぐ",
      version: 2,
      plans: [plan(1, "権限表を一箇所にまとめ、画面ごとの判定を表から引く。")],
    }),
    mk("C-5", "ビルドの高速化", "in_progress", {
      priority: "P2",
      flags: { running: true },
      runs: [run("R-7", "C-5")],
      headline: "ビルド時間を半分にする",
    }),
    mk("C-6", "古いバッチの撤去", "in_progress", {
      priority: "P2",
      headline: "夜間の集計バッチを止め、ジョブ定義を消す",
      flags: { running: true, unresponsive: true },
      runs: [run("R-8", "C-6", { started_at: iso(200) })],
    }),
    mk("C-7", "料金表の計算を直す", "awaiting_completion_approval", {
      priority: "P0",
      version: 4,
      description: "端数の丸めが請求書とずれる。",
      done_criteria: "請求書の金額と画面の金額が全件一致する。",
      headline: "料金表の丸めを請求書とそろえる",
      context: "月次請求の締め前に必要。経理の突合作業が減る。",
      stages: {
        ...NO_STAGES,
        expected: [
          {
            kind: "ui",
            scene: "料金表の画面",
            before: "請求書と 1 円ずれる",
            after: "請求書と同じ金額が出る",
          },
          {
            kind: "cli",
            scene: "突合コマンド",
            before: "差分が出る",
            after: "差分が 0 件",
          },
          {
            kind: "behavior",
            scene: "月次の締め処理",
            before: "手で直す",
            after: "そのまま通る",
          },
        ],
        // 実物は 2 件だけ。3 件目は「実物なし」の行になる。
        actual: [
          { scene: "料金表の画面", after: "請求書と同じ金額が出た" },
          { scene: "突合コマンド", after: "差分は 0 件だった" },
        ],
      },
      operations: [
        op("O-1", "C-7", "release", "pending", "v1.4.0 を本番へ出す"),
      ],
    }),
    mk("C-8", "移行スクリプトの失敗調査", "awaiting_human", {
      priority: "P1",
      headline: "旧テーブルの移行が 3 割で止まる原因を突き止める",
      version: 2,
      holds: [
        {
          answer: null,
          answered_at: null,
          answered_by: null,
          answered_channel: null,
          answered_verification: null,
          relay_quote: null,
          relay_session: null,
          from_status: "in_progress",
          from_status_label: "着手中",
          question:
            "旧テーブルの deleted_at が NULL の行を移行対象に含めてよいですか？ 含めると 3 万件増えます。",
          raised_at: iso(90),
          run_id: null,
        },
      ],
    }),
    mk("C-9", "画像変換のコスト超過", "in_progress", {
      priority: "P1",
      headline: "検索結果のサムネイル生成を安い方式へ替える",
      flags: {
        budget: {
          challenge_id: "C-9",
          impl_remaining_usd: 0,
          plan_version: 1,
          review_remaining_usd: 3.5,
        },
      },
    }),
    mk("C-10", "外部 Issue 由来の課題", "in_progress", {
      priority: "P2",
      title:
        "<img src=x onerror=alert(1)> 外部 Issue のタイトルをそのまま取り込んだ",
      description: LONG,
      done_criteria: LONG,
      context: LONG,
      flags: { discrepancy: ["upstream_updated", "upstream_commented"] },
    }),
    mk("C-11", "決済画面の改修", "verifying", {
      priority: "P2",
      headline: "決済画面を新しいビルド基盤でビルドし直す",
      flags: {
        waitingCi: {
          checks: "ci/unit: pending, ci/e2e: pending",
          pr_url: "https://example.invalid/pr/88",
        },
      },
    }),
    mk("C-12", "古い設定の削除", "done", {
      priority: "P2",
      version: 6,
      headline: "使われていない設定キーを消す",
    }),
    mk("C-13", "トリアージ待ちの run", "in_progress", {
      priority: "P1",
      headline: "旧バッチを呼ぶ API の残りを洗い出す",
      flags: {
        triage: {
          reason: "run が 3 回連続で同じテストで落ちた。仕様の食い違いの疑い。",
          run_id: "R-9",
        },
      },
    }),
    // 関係グラフの案（flywheel#176）で親の大枠として見せる課題。人間の操作は待たない。
    mk("C-14", "旧基盤の撤去", "in_progress", {
      priority: "P2",
      headline: "旧バッチ基盤を止めて消す",
      context:
        "全体計画「運用費の削減」の 2 段目。旧バッチ基盤は月 40 万円の固定費があり、夜間の障害の半分を占める。ここを消すと、後の「監視の一本化」で見る対象が半分になる。設定の削除が済み、いまはバッチ本体の撤去と、移行の失敗の調査を並べて進めている。",
    }),
    mk("C-15", "ビルドと配布の改善", "classified", {
      priority: "P2",
      headline: "ビルドを速くし、配布を手順書なしで回せるようにする",
      context:
        "開発の待ち時間を減らす計画の土台。検索 API のページング（C-2）の配布もここで整えた手順に乗る。",
    }),
    mk("C-16", "依存のキャッシュ", "classified", {
      priority: "P2",
      headline: "依存の取得を毎回やり直さない",
      context: "ビルドの高速化のうち、取得の待ちを削る部分。",
    }),
    mk("C-17", "キャッシュキーの設計", "in_progress", {
      priority: "P2",
      headline: "ロックファイルの差だけでキャッシュを捨てる",
      flags: { running: true },
      runs: [run("R-10", "C-17", { started_at: iso(15) })],
    }),
    // 先行が終わるのを待つ後続（依存の後続は、先行が完了するまで分類済か計画承認待ちに留まる）
    mk("C-18", "旧テーブルの削除", "classified", {
      priority: "P2",
      headline: "移行が済んだ旧テーブルを消す",
    }),
    mk("C-19", "配布手順の更新", "classified", {
      priority: "P2",
      headline: "新しいビルドの成果物を出す手順に書き換える",
    }),
    // 自動では解けない外部待ちの例（他チームへの相談待ち。案の印 waiting_party を graph.ts が持つ）
    mk("C-20", "返金 API の上限の確認", "in_progress", {
      priority: "P1",
      headline: "決済チームに返金 API の上限を問い合わせている",
    }),
  ];
  const beta: MockChallenge[] = [
    mk("C-1", "レポートの CSV 出力", "classified", {
      priority: "P2",
      headline: "月次レポートを表計算で開ける形でも出す",
    }),
    mk("C-2", "デプロイ手順の整理", "awaiting_plan_approval", {
      priority: "P1",
      headline: "手作業の残るデプロイ手順を 1 本にまとめる",
      plans: [plan(1, "手順書を 1 本にまとめ、手作業の箇所を表にする。")],
    }),
    mk("C-3", "監視のしきい値見直し", "awaiting_completion_approval", {
      priority: "P1",
      headline: "誤検知の多い警報のしきい値を上げる",
      version: 3,
      done_criteria: "誤検知が週 5 件以下になる。",
      operations: [
        op("O-1", "C-3", "release", "pending", "監視設定を本番へ反映"),
        op("O-2", "C-3", "notify", "pending", "運用チームへ変更を通知"),
      ],
    }),
  ];
  const ok = (name: string): Workspace => ({
    error: null,
    name,
    path: `/mock/${name}`,
    state: "ok",
  });
  return {
    alpha: {
      meta: ok("alpha"),
      items: alpha,
      cycle: {
        state: "running",
        last_started_at: iso(12),
        last_ended_at: iso(90),
        last_result: "completed",
      },
      graph: graphs.alpha,
      icon: "🚀",
    },
    beta: {
      meta: ok("beta"),
      items: beta,
      cycle: {
        state: "idle",
        last_started_at: iso(2010),
        last_ended_at: iso(2000),
        last_result: "completed",
      },
      graph: graphs.beta,
      icon: BETA_ICON,
    },
    gamma: {
      meta: {
        error: "ストアが見つかりません",
        name: "gamma",
        path: "/mock/gamma",
        state: "store_not_found",
      },
      items: [],
      cycle: {
        state: "idle",
        last_started_at: null,
        last_ended_at: null,
        last_result: null,
      },
      graph: { relations: [], repos: [], waiting_party: [] },
      icon: null,
    },
  };
}

// --- 導出（本物では core が決定的に導く値を、ここでは仕様の表のとおりに写す） ---

const HUMAN_STATES = [
  "awaiting_plan_approval",
  "awaiting_completion_approval",
  "awaiting_human",
];

// 区分の型（labels.ts の DiscrepancyKind）から引く。型の無い文字列で書かない。
const CYCLE_KIND: DiscrepancyKind = "dependency_cycle";

export function cardOf(m: MockChallenge): Card {
  const f = m.flags;
  // 種類が dependency_cycle だけの食い違いは印と review_discrepancy に数えない（M45H19。core と同じ）。
  const disc =
    f.discrepancy !== undefined &&
    (f.discrepancy.length === 0 || f.discrepancy.some((k) => k !== CYCLE_KIND));
  const modifiers: string[] = [];
  if (f.running) modifiers.push("running");
  if (f.unresponsive) modifiers.push("unresponsive");
  if (f.waitingCi) modifiers.push("waiting_ci");
  if (f.budget) modifiers.push("budget_exhausted");
  if (disc) modifiers.push("discrepancy");
  const actions: string[] = [];
  const s = m.c.status;
  if (s === "awaiting_plan_approval") actions.push("approve_plan");
  if (s === "awaiting_completion_approval") actions.push("approve_completion");
  if (s === "awaiting_human") actions.push("answer_question");
  if (f.triage) actions.push("triage");
  if (f.budget) actions.push("increase_budget");
  if (m.operations.some((o) => o.state === "pending"))
    actions.push("approve_operation");
  if (disc) actions.push("review_discrepancy");
  if (f.slot) actions.push("clear_slot");
  return {
    context: m.context,
    headline: m.headline,
    modifiers,
    next_human_actions: actions,
    slot_ids: f.slot ? [f.slot.slot_id] : [],
    state: s,
  };
}

export const toCard = (m: MockChallenge): ChallengeCard => ({
  ...m.c,
  card: cardOf(m),
});

export function statusOf(w: MockWorkspace): StatusResponse {
  const items = w.items;
  const nhChallenges = items.filter((m) => HUMAN_STATES.includes(m.c.status));
  const humanIds = new Set(nhChallenges.map((m) => m.c.id));
  const actionable = items.filter(
    (m) =>
      m.c.status !== "done" &&
      !humanIds.has(m.c.id) &&
      cardOf(m).next_human_actions.length === 0,
  );
  const ops = (state: string) =>
    items.flatMap((m) => m.operations.filter((o) => o.state === state));
  return {
    actionable: {
      challenges: actionable.map((m) => ({ ...m.c, unmet_dependencies: [] })),
    },
    approved: { operations: ops("approved") },
    needs_human: {
      budget_exhausted: items.flatMap((m) =>
        m.flags.budget ? [m.flags.budget] : [],
      ),
      challenges: nhChallenges.map((m) => m.c),
      discrepancies: items.flatMap((m) =>
        m.flags.discrepancy
          ? [
              {
                challenge_id: m.c.id,
                kinds: m.flags.discrepancy,
                ...(m.flags.discrepancy.length > 0 &&
                m.flags.discrepancy.every((k) => k === CYCLE_KIND)
                  ? { cycle_only: true }
                  : {}),
              },
            ]
          : [],
      ),
      operations: ops("pending"),
      slots: items.flatMap((m) =>
        m.flags.slot ? [{ ...m.flags.slot }].map((s) => ({ ...s })) : [],
      ),
      triage: items.flatMap((m) =>
        m.flags.triage ? [{ challenge_id: m.c.id, ...m.flags.triage }] : [],
      ),
    },
    waiting_external: {
      challenges: items.flatMap((m) =>
        m.flags.waitingCi
          ? [{ challenge_id: m.c.id, ...m.flags.waitingCi }]
          : [],
      ),
    },
  };
}

// --- ストア本体 ---

export class MockStore {
  workspaces: Record<string, MockWorkspace> = seed();
  private listeners = new Set<(ws: string) => void>();

  subscribe(fn: (ws: string) => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }
  emit(ws: string): void {
    for (const fn of this.listeners) fn(ws);
  }
}
