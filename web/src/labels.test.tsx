import { expect, it } from "vitest";
import {
  CARD_ACTION_CODES,
  CARD_MODIFIER_CODES,
  DISCREPANCY_KIND_CODES,
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
  DISCREPANCY_KINDS,
  DISCREPANCY_LABELS,
  HOLD_KIND_HUMAN,
  HOLD_KIND_PARENT_DIRECT,
  MARK_KINDS,
  MARK_LABELS,
  MODIFIER_LABELS,
  PHASE_LABELS,
  STATE_LABELS,
  TASK_PHASES,
} from "./labels.ts";

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
