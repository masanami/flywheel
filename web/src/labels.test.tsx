import { expect, it } from "vitest";
import {
  CARD_ACTION_CODES,
  CARD_MODIFIER_CODES,
  DISCREPANCY_KIND_CODES,
  EXECUTION_RESULT_CODES,
  HOLD_KIND_CODES,
  STATUS_CODES,
  TASK_PHASE_CODES,
} from "./api/types.gen.ts";
import {
  ACTION_LABELS,
  CARD_ACTIONS,
  CARD_MODIFIERS,
  CARD_STATES,
  CYCLE_STATES,
  CYCLE_STATE_LABELS,
  CYCLE_WORK_STAGES,
  CYCLE_WORK_STAGE_LABELS,
  DISCREPANCY_KINDS,
  DISCREPANCY_LABELS,
  EXECUTION_RESULT_LABELS,
  HOLD_KIND_HUMAN,
  HOLD_KIND_PARENT_DIRECT,
  MARK_KINDS,
  MARK_LABELS,
  MODIFIER_LABELS,
  PHASE_LABELS,
  STATE_LABELS,
  TASK_PHASES,
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
    running: "サイクル: 走っている",
    stale: "サイクル: 止まっているかもしれない",
  });
});

it("#805: サイクルを回すと進むものの 8 つの段すべてに語があり、状況の API の段のキーと一致する", () => {
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
