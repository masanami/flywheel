// ブラウザ内の偽のストア。開発時の mock モードだけが読み込む（本番のビルドには入らない）。
// 返す形は types.gen.ts と仕様の API の節に従う。状態の導出は本物では core がやるが、ここでは同じ表を写す。

import type {
  Approval,
  BudgetExhausted,
  Card,
  Challenge,
  ChallengeCard,
  CycleStatus,
  DirectCompletion,
  GraphResponse,
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
import { type GraphSeed, seedGraphs } from "./graph.ts";
import { type LargeSpec, largeWorkspace } from "./large.ts";

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
  // 親が直接行った完了の記録（無ければ空）
  directCompletions: DirectCompletion[];
  // 完了確認待ちの検証が「本番反映で満たされる見込み」と区分した条件（無ければ空）
  conditionsAfterRelease: string[];
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
  // 関係グラフの偽の関係（親子・依存）。応答は graphOf が課題の導出と合わせて作る
  graph: GraphSeed;
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
  withdrawn: "取り下げ",
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
    directCompletions,
    conditionsAfterRelease,
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
    directCompletions: directCompletions ?? [],
    conditionsAfterRelease: conditionsAfterRelease ?? [],
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
  flywheel_version: null,
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
      conditionsAfterRelease: ["本番の料金表の画面で、請求書と同じ金額が出る"],
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
          closed_at: null,
          relay_quote: null,
          relay_session: null,
          from_status: "in_progress",
          from_status_label: "着手中",
          kind: "human",
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
      // 切り詰めた長い文章に「全文を開く」が出ることを実ブラウザで確かめるための、長い「やらないこと」
      stages: { ...NO_STAGES, non_goals: [LONG] },
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
    // 取り下げた課題。動いている課題（C-18）の前提なので、既定でも灰色で出る（graph.ts）
    mk("C-21", "旧テーブルの参照元の洗い出し", "withdrawn", {
      priority: "P2",
      headline: "別の調査で済んだので取り下げた",
    }),
    // 何ともつながらない完了の課題。既定では隠れ、「終わった課題もすべて表示」で出る
    mk("C-22", "ログ出力の整理", "done", {
      priority: "P3",
      version: 4,
      headline: "不要なログ出力を削った",
    }),
    // 注目の課題とどの線でもつながらない TODO（分類待ち・計画待ち）のうち、優先度が P1 以下のものは「ほかの課題」の大枠に
    // まとまる（既定で畳む）。C-23 → C-24 の依存のまとまりごと入る。優先度が P0 の C-25 は、線が無くても大枠に入らず前に出る
    mk("C-23", "エラーページの文言の見直し", "unclassified", {
      priority: "P3",
      urgency: "低",
      description: "404 と 500 のページの文言がそっけない。",
    }),
    mk("C-24", "CSV 出力の文字コードの指定", "classified", {
      priority: "P2",
      headline: "出力時に文字コードを選べるようにする",
    }),
    // 人間の操作待ちの親 C-3 の子（着手中）。C-3 を開くと、大枠の見出しの右上にもチップが出る（親を開くまで中は見えない）
    mk("C-26", "再送の重複テストの追加", "in_progress", {
      priority: "P2",
      headline: "冪等キーの再送が 1 通だけ届くことを確かめるテストを足す",
    }),
    // C-1 の子（C-2 から見て孫）。C-2 は実施中の C-5 から依存で 2 段先（C-5 → C-19 → C-2）。
    // 注目は推移的なので、注目に当たらない子 C-1・孫 C-27 も前に出る
    mk("C-27", "ログイン画面の文言の翻訳", "unclassified", {
      headline: "英語の文言も同じ言い回しにそろえる",
    }),
    mk("C-25", "決済 API の障害の一次対応", "unclassified", {
      priority: "P0",
      urgency: "高",
      description: "決済 API が断続的に 500 を返している。",
    }),
    // 人間の操作待ちのチップが多い課題（flywheel#464）。実施中のまま、トリアージ・予算・操作の承認・食い違いの
    // 4 つを待つ。広い幅のノード・一覧の行で、チップが題名や「▶ 実施中」と重ならないかを見る
    mk("C-28", "請求データの夜間集計を新しい基盤へ載せ替える", "in_progress", {
      priority: "P1",
      headline: "夜間集計を新基盤で回し、旧基盤の負荷を下げる",
      flags: {
        running: true,
        triage: {
          reason: "run が 2 回続けて同じ集計の検査で落ちた。",
          run_id: "R-11",
        },
        budget: {
          challenge_id: "C-28",
          impl_remaining_usd: 0,
          plan_version: 1,
          review_remaining_usd: 2,
        },
        discrepancy: ["upstream_updated"],
      },
      runs: [run("R-11", "C-28", { started_at: iso(20) })],
      operations: [
        op("O-1", "C-28", "release", "pending", "集計の切り替えを本番へ反映"),
      ],
    }),
    // チップが多い親（flywheel#464）。完了の承認・トリアージ・予算・操作の承認・食い違いの 5 つを待ちながら自身も実施中で、
    // 実施中の子 C-30 を持つので既定で開く。開いた大枠の見出しで、チップが題名や「▶ 実施中」と重ならないかを見る
    mk(
      "C-29",
      "問い合わせ窓口の一本化と旧フォームの撤去",
      "awaiting_completion_approval",
      {
        priority: "P1",
        headline: "窓口を 1 つにまとめ、古いフォームを消す",
        flags: {
          running: true,
          triage: {
            reason: "子の run が旧フォームの削除で止まった。",
            run_id: "R-12",
          },
          budget: {
            challenge_id: "C-29",
            impl_remaining_usd: 0,
            plan_version: 2,
            review_remaining_usd: 1,
          },
          discrepancy: ["upstream_commented"],
        },
        runs: [run("R-12", "C-29", { started_at: iso(25) })],
        operations: [
          op("O-1", "C-29", "release", "pending", "旧フォームを本番から外す"),
        ],
      },
    ),
    mk("C-30", "旧フォームの送信先の付け替え", "in_progress", {
      priority: "P2",
      headline: "旧フォームの送信を新しい窓口へ流す",
      flags: { running: true },
      runs: [run("R-13", "C-30", { started_at: iso(10) })],
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
      graph: { parents: {}, deps: [] },
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

// 人間対応待ちで、いま開いている保留の種類が親が直接行う（core の listParentAgentChallenges と同じ規則）。
export function isParentAgent(m: MockChallenge): boolean {
  if (m.c.status !== "awaiting_human") return false;
  const open = m.holds.filter((h) => h.answer === null && h.closed_at === null);
  return open[open.length - 1]?.kind === "parent_direct";
}

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
  if (s === "awaiting_human" && !isParentAgent(m))
    actions.push("answer_question");
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

// `GET …/graph` の偽の応答。区分・running・stale は課題のカードの導出と同じ規則（人間 > 完了 > 取り下げ > システム。
// 外部待ちは core から出ない）。登録の日時は 2026-10-10T00:00:00Z からの経過分。
export function graphOf(w: MockWorkspace): GraphResponse {
  const byId = new Map(w.items.map((m) => [m.c.id, m]));
  const idNum = (id: string) => Number(id.replace(/^\D+/, ""));
  const nodes = [...w.items]
    .sort((a, b) => idNum(a.c.id) - idNum(b.c.id))
    .map((m) => {
      const card = cardOf(m);
      const category =
        card.next_human_actions.length > 0
          ? "human"
          : isParentAgent(m)
            ? "parent_agent"
            : m.c.status === "done"
              ? "done"
              : m.c.status === "withdrawn"
                ? "withdrawn"
                : "system";
      return {
        category,
        context: m.context,
        headline: m.headline,
        id: m.c.id,
        next_human_actions: card.next_human_actions,
        parent_id: w.graph.parents[m.c.id] ?? null,
        priority: m.c.priority,
        running: !!(m.flags.running || m.flags.unresponsive),
        stale: !!m.flags.unresponsive,
        state: card.state,
        title: m.c.title,
      };
    });
  const edges = [...w.graph.deps]
    .sort((a, b) => idNum(a.from) - idNum(b.from) || idNum(a.to) - idNum(b.to))
    .map((d) => ({
      from: d.from,
      kind: d.kind,
      registered_at: new Date(
        Date.parse("2026-10-10T00:00:00Z") + d.minutes * 60_000,
      ).toISOString(),
      registered_via: d.via,
      satisfied: byId.get(d.from)?.c.status === "done",
      to: d.to,
    }));
  return { edges, nodes };
}

export const toCard = (m: MockChallenge): ChallengeCard => ({
  ...m.c,
  card: cardOf(m),
});

export function statusOf(w: MockWorkspace): StatusResponse {
  const items = w.items;
  const parentAgent = items.filter(isParentAgent);
  const nhChallenges = items.filter(
    (m) => HUMAN_STATES.includes(m.c.status) && !isParentAgent(m),
  );
  const humanIds = new Set(
    [...nhChallenges, ...parentAgent].map((m) => m.c.id),
  );
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
    parent_agent: { challenges: parentAgent.map((m) => m.c) },
    waiting_external: {
      challenges: items.flatMap((m) =>
        m.flags.waitingCi
          ? [{ challenge_id: m.c.id, ...m.flags.waitingCi }]
          : [],
      ),
    },
  };
}

// 大きなまとまりの偽のデータ（flywheel#470。`?mock=large` のときだけ）。large は課題 100 件がほぼ 1 つにつながる形、
// large-flat は同じ 100 件から線をすべて抜いた形（描画の速さの比べ先）。
export function seedLarge(): Record<string, MockWorkspace> {
  const build = (s: LargeSpec): MockChallenge =>
    mk(s.id, s.title, s.status, {
      priority: s.priority,
      flags: { running: s.running, unresponsive: s.unresponsive },
      operations: s.operation
        ? [op(`O-${s.id.slice(2)}`, s.id, "release", "pending", "本番へ反映")]
        : [],
    });
  const base = {
    cycle: {
      state: "running",
      last_started_at: iso(12),
      last_ended_at: iso(90),
      last_result: "completed",
    },
  };
  return {
    large: largeWorkspace("large", true, build, base),
    "large-flat": largeWorkspace("large-flat", false, build, base),
  };
}

// --- ストア本体 ---

export class MockStore {
  workspaces: Record<string, MockWorkspace>;
  constructor(workspaces: Record<string, MockWorkspace> = seed()) {
    this.workspaces = workspaces;
  }
  private listeners = new Set<(ws: string) => void>();

  subscribe(fn: (ws: string) => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }
  emit(ws: string): void {
    for (const fn of this.listeners) fn(ws);
  }
}
