import { expect, it } from "vitest";
import {
  ARTIFACT_KIND_CODES,
  CARD_ACTION_CODES,
  CARD_MODIFIER_CODES,
  CHALLENGE_PROGRESS_CODES,
  CONFLICT_FIX_TARGET_CODES,
  DELEGATE_STATE_CODES,
  DISCREPANCY_KIND_CODES,
  EXECUTION_RESULT_CODES,
  EXECUTION_RESULT_WORDS,
  EXPECTED_KIND_CODES,
  HOLD_KIND_CODES,
  OPERATION_KIND_CODES,
  OPERATION_STATE_CODES,
  REQUESTED_KIND_CODES,
  STATUS_CODES,
  TASK_LANDING_CODES,
  TASK_PHASE_CODES,
  TASK_PROGRESS_CODES,
  VERDICT_CODES,
} from "./api/types.gen.ts";
import {
  ACTION_LABELS,
  ARTIFACT_KIND_LABELS,
  CARD_ACTIONS,
  CARD_MODIFIERS,
  CARD_STATES,
  CHALLENGE_PROGRESS_LABELS,
  CONFLICT_FIX_TARGET_LABELS,
  CYCLE_STATES,
  CYCLE_STATE_LABELS,
  CYCLE_STATE_SHORT_LABELS,
  CYCLE_WORK_STAGES,
  CYCLE_WORK_STAGE_LABELS,
  DELEGATE_STATE_LABELS,
  DISCREPANCY_KINDS,
  DISCREPANCY_LABELS,
  EXECUTION_RESULT_LABELS,
  EXPECTED_KIND_LABELS,
  HISTORY_ACTION_LABELS,
  HOLD_KIND_HUMAN,
  HOLD_KIND_PARENT_DIRECT,
  LANDING_LABELS,
  MARK_KINDS,
  MARK_LABELS,
  MODIFIER_LABELS,
  NOT_STARTED_REASON_LABELS,
  OPERATION_KIND_LABELS,
  OPERATION_STATE_LABELS,
  PHASE_LABELS,
  PHASE_LEGEND_LABELS,
  REQUESTED_KIND_LABELS,
  STATE_LABELS,
  TASK_PHASES,
  TASK_PROGRESS_LABELS,
  VERDICT_LABELS,
  WHO_ME_TTY,
  artifactKindLabel,
  label,
} from "./labels.ts";
import { emptyStatus } from "./testing/fakes.ts";

it("AC-102: 閉集合のすべてのコードに文言があり、core の定義と一致する", () => {
  expect([...CARD_ACTIONS]).toEqual([...CARD_ACTION_CODES]);
  expect([...CARD_MODIFIERS]).toEqual([...CARD_MODIFIER_CODES]);
  expect([...CARD_STATES]).toEqual([...STATUS_CODES]);
  expect([...DISCREPANCY_KINDS]).toEqual([...DISCREPANCY_KIND_CODES]);
  expect([HOLD_KIND_HUMAN, HOLD_KIND_PARENT_DIRECT]).toEqual([
    ...HOLD_KIND_CODES,
  ]);
  expect(HOLD_KIND_PARENT_DIRECT).toBe("parent_direct");
  for (const [codes, table] of [
    [CARD_STATES, STATE_LABELS],
    [CARD_MODIFIERS, MODIFIER_LABELS],
    [CARD_ACTIONS, ACTION_LABELS],
    [DISCREPANCY_KINDS, DISCREPANCY_LABELS],
  ] as const) {
    for (const c of codes) {
      expect((table as Record<string, string>)[c], c).toBeTruthy();
    }
  }
});

it("反映の試行の結果のすべての値に語があり、S3a の語は親の文のとおり", () => {
  expect(Object.keys(EXECUTION_RESULT_LABELS).sort()).toEqual(
    [...EXECUTION_RESULT_CODES].sort(),
  );
  for (const c of EXECUTION_RESULT_CODES) {
    expect(EXECUTION_RESULT_LABELS[c], c).toBeTruthy();
  }
  // 語の正本は Go の表（生成物の EXECUTION_RESULT_WORDS）。画面の表は、同じ添字の語をそのまま持つ。
  expect(EXECUTION_RESULT_WORDS).toHaveLength(EXECUTION_RESULT_CODES.length);
  EXECUTION_RESULT_CODES.forEach((c, i) => {
    expect(EXECUTION_RESULT_LABELS[c], c).toBe(EXECUTION_RESULT_WORDS[i]);
  });
  expect(EXECUTION_RESULT_LABELS.upstream_failed).toBe("上流を読めなかった");
  expect(EXECUTION_RESULT_LABELS.landed).toBe("委譲先が反映先へ入れた");
  expect(EXECUTION_RESULT_LABELS.land_refused).toBe("委譲先が入れられなかった");
  expect(EXECUTION_RESULT_LABELS.delegate_ended).toBe(
    "委譲先の実行が、入れずに終わった",
  );
});

it("区分の印の 6 種すべてに文言がある", () => {
  expect(MARK_KINDS).toHaveLength(6);
  for (const k of MARK_KINDS) expect(MARK_LABELS[k], k).toBeTruthy();
});

it("人に見せる語に「CI」を使わない（flywheel は汎用の道具。#176）", () => {
  for (const table of [
    STATE_LABELS,
    MODIFIER_LABELS,
    ACTION_LABELS,
    MARK_LABELS,
  ])
    for (const v of Object.values(table)) expect(v).not.toMatch(/\bCI\b/);
});

it("M4H34: unresponsive の文言は「更新なし」で、「応答なし」を使わない", () => {
  expect(MODIFIER_LABELS.unresponsive).toContain("更新なし");
  expect(MARK_LABELS.unresponsive).toBe("更新なし");
  expect(MODIFIER_LABELS.unresponsive).not.toContain("応答なし");
  expect(MARK_LABELS.unresponsive).not.toContain("応答なし");
});

it("表示の区分（phase）の全部の値に文言があり、core の定義と一致する", () => {
  expect([...TASK_PHASES]).toEqual([...TASK_PHASE_CODES]);
  for (const p of TASK_PHASES) expect(PHASE_LABELS[p], p).toBeTruthy();
});

it("#803: サイクルの状態（state）の全部の値に文言がある（値は core の idle・running・stale）", () => {
  expect([...CYCLE_STATES].sort()).toEqual(["idle", "running", "stale"]);
  for (const c of CYCLE_STATES) expect(CYCLE_STATE_LABELS[c], c).toBeTruthy();
  expect(CYCLE_STATE_LABELS).toEqual({
    idle: "サイクル: 走っていない",
    running: "サイクル: 実行中",
    stale: "サイクル: 止まっているかもしれない",
  });
});

it("#805: サイクルを回すと進むものの 9 つの段すべてに語があり、状況の API の段のキーと一致する", () => {
  const keys = Object.keys(emptyStatus.cycle_work.stages).sort();
  expect([...CYCLE_WORK_STAGES].sort()).toEqual(keys);
  for (const s of CYCLE_WORK_STAGES) {
    expect(CYCLE_WORK_STAGE_LABELS[s], s).toBeTruthy();
  }
  expect(CYCLE_WORK_STAGES.map((s) => CYCLE_WORK_STAGE_LABELS[s])).toEqual([
    "反映",
    "分類",
    "計画",
    "委譲",
    "完了の検証",
    "学び",
    "改修の提案",
    "片付け",
    "スーパーバイザー待ち",
  ]);
});

// #814 掃除 5: label() は表の自前のキーだけを引く。継承されたキーの名前の値は、そのまま文字で返る。
it("#814: label() は、constructor・toString・__proto__ のような名前の値を、そのまま文字で返す", () => {
  const table = { a: "あ" };
  expect(label(table, "a")).toBe("あ");
  for (const code of ["constructor", "toString", "hasOwnProperty", "__proto__"])
    expect(label(table, code), code).toBe(code);
  expect(label(table, "未知")).toBe("未知");
});

// #906: 統合先の識別を初めて記録した作業ログの action（integration_ref_set）に、経緯の文言がある。
it("#906: 経緯の action integration_ref_set に文言がある", () => {
  expect(HISTORY_ACTION_LABELS.integration_ref_set).toBe("統合先を記録した");
});

// #1334: 仕様の下書きを消せなかった作業ログの action と、委譲を起動しない理由 spec_draft_changed に文言がある。
it("#1334: 経緯の action spec_draft_cleanup_failed に文言がある", () => {
  expect(HISTORY_ACTION_LABELS.spec_draft_cleanup_failed).toBe(
    "仕様の下書きを消せなかった",
  );
});

it("#1334: 委譲を起動しない理由 spec_draft_changed に文言がある", () => {
  expect(NOT_STARTED_REASON_LABELS.spec_draft_changed).toBe(
    "仕様の下書きが計画の後に変わった",
  );
});

// 値の集合は core の閉集合（types.gen.ts の *_CODES）で、語の表は全部の値に語を持つ（過不足なし）。
const coversAll = (table: Record<string, string>, codes: readonly string[]) => {
  expect(Object.keys(table).sort()).toEqual([...codes].sort());
  for (const c of codes) expect(table[c], c).toBeTruthy();
};

it("AC-81 #927: verdict の 3 値・landing の 3 値・委譲先の実行の状態の 5 値の語がある", () => {
  coversAll(VERDICT_LABELS, VERDICT_CODES);
  coversAll(LANDING_LABELS, TASK_LANDING_CODES);
  coversAll(DELEGATE_STATE_LABELS, DELEGATE_STATE_CODES);
  expect(VERDICT_LABELS.met).toBe("満たした");
  expect(LANDING_LABELS).toEqual({
    verified: "検証済み",
    sent: "統合中",
    landed: "統合済み",
  });
  expect(DELEGATE_STATE_LABELS).toEqual({
    running: "実行中",
    waiting: "待機中",
    succeeded: "成功",
    failed: "失敗",
    cancelled: "中止",
  });
});

it("AC-101〜104 #927: phase の札は 実行中・あなた待ち・次に実行・前提待ち（済み・取りやめは変えない）", () => {
  expect(PHASE_LABELS.running).toBe("実行中");
  expect(PHASE_LABELS.waiting).toBe("あなた待ち");
  expect(PHASE_LABELS.next).toBe("次に実行");
  expect(PHASE_LABELS.blocked).toBe("前提待ち");
  expect(PHASE_LABELS.done).toBe("済み");
  expect(PHASE_LABELS.cancelled).toBe("取りやめ");
});

it("AC-105 #927: 語の表のどこにも「走っている」の語は無い", () => {
  const tables: Record<string, string>[] = [
    PHASE_LABELS,
    PHASE_LEGEND_LABELS,
    CYCLE_STATE_LABELS,
    CYCLE_STATE_SHORT_LABELS,
    TASK_PROGRESS_LABELS,
    CHALLENGE_PROGRESS_LABELS,
    DELEGATE_STATE_LABELS,
  ];
  for (const t of tables) {
    for (const v of Object.values(t)) expect(v).not.toContain("走っている");
  }
});

it("AC-108 #927: progress の全部の値（タスクの 4 つ・課題の 2 つ）の語がある", () => {
  coversAll(TASK_PROGRESS_LABELS, TASK_PROGRESS_CODES);
  coversAll(CHALLENGE_PROGRESS_LABELS, CHALLENGE_PROGRESS_CODES);
  expect(TASK_PROGRESS_LABELS).toEqual({
    working: "作業中",
    verifying: "検証待ち",
    verified: "検証済み",
    landing: "統合中",
  });
  expect(CHALLENGE_PROGRESS_LABELS).toEqual({
    reflecting: "反映中",
    checking: "全体の確認中",
  });
});

it("AC-122 #927: 不可逆操作の状態の全部の値の語がある", () => {
  coversAll(OPERATION_STATE_LABELS, OPERATION_STATE_CODES);
  expect(OPERATION_STATE_LABELS).toEqual({
    pending: "承認待ち",
    approved: "承認済み",
    rejected: "差し戻し",
    executed: "実行済み",
    closed: "終了",
  });
});

it("AC-123 #927: 不可逆操作の種類の全部の値の語がある", () => {
  coversAll(OPERATION_KIND_LABELS, OPERATION_KIND_CODES);
  expect(OPERATION_KIND_LABELS).toEqual({
    release: "反映",
    delete: "削除",
    external_send: "外部送信",
    other: "その他",
  });
});

it("#927: 委譲先が求めていること（requested_kind）の語は、括弧の補足を付けない", () => {
  // core が受け付ける requested_action.kind（answer・approve・observe・review）の全部に語がある。
  expect(Object.keys(REQUESTED_KIND_LABELS).sort()).toEqual([
    "answer",
    "approve",
    "observe",
    "review",
  ]);
  for (const v of Object.values(REQUESTED_KIND_LABELS)) {
    expect(v).not.toMatch(/[（(]/);
  }
});

it("AC-106 #927: 語の表の語に「端末」は無い", () => {
  const all = JSON.stringify([
    WHO_ME_TTY,
    CYCLE_STATE_LABELS,
    PHASE_LABELS,
    HISTORY_ACTION_LABELS,
  ]);
  expect(all).not.toContain("端末");
});

it("#963: 成果物の種類の語（プルリクエスト・ブランチ・コミット）と、知らない値の出し方", () => {
  expect(ARTIFACT_KIND_LABELS).toEqual({
    pr: "プルリクエスト",
    branch: "ブランチ",
    commit: "コミット",
  });
  expect(artifactKindLabel("pr")).toBe("プルリクエスト");
  expect(artifactKindLabel("zzz")).toBe("成果物（zzz）");
  expect(artifactKindLabel("constructor")).toBe("成果物（constructor）");
});

it("#964: 委譲先が求めていること・コンフリクトの解消の宛先・予想の種類の全部の値に語がある（core の閉集合から生成した値の一覧で漏れを検出する）", () => {
  coversAll(REQUESTED_KIND_LABELS, REQUESTED_KIND_CODES);
  coversAll(CONFLICT_FIX_TARGET_LABELS, CONFLICT_FIX_TARGET_CODES);
  coversAll(ARTIFACT_KIND_LABELS, ARTIFACT_KIND_CODES);
  coversAll(EXPECTED_KIND_LABELS, EXPECTED_KIND_CODES);
  expect(EXPECTED_KIND_LABELS).toEqual({
    ui: "画面",
    cli: "コマンド",
    behavior: "振る舞い",
  });
});
