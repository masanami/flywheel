import { expect, it } from "vitest";
import type { ChallengeCard } from "../api/types.gen.ts";
import { faceHeadline, markContext, marksFor } from "./marks.ts";

const card = (
  id: string,
  c: { modifiers?: string[]; next_human_actions?: string[] } = {},
): ChallengeCard =>
  ({
    id,
    card: {
      context: null,
      headline: null,
      modifiers: [],
      next_human_actions: [],
      state: "in_progress",
      ...c,
    },
  }) as unknown as ChallengeCard;

const status = (actionable: string[], approved: string[]) =>
  ({
    actionable: { challenges: actionable.map((id) => ({ id })) },
    approved: {
      operations: approved.map((challenge_id) => ({ challenge_id })),
    },
  }) as never;

it("AC-188〜193: 6 種の印を既存の API の値から導く", () => {
  const ctx = markContext(status(["C-3"], ["C-6"]));
  expect(
    marksFor(card("C-1", { next_human_actions: ["approve_plan"] }), ctx),
  ).toEqual(["needs_human"]);
  expect(marksFor(card("C-2", { modifiers: ["waiting_ci"] }), ctx)).toEqual([
    "external",
  ]);
  expect(marksFor(card("C-3"), ctx)).toEqual(["actionable"]);
  expect(marksFor(card("C-4", { modifiers: ["running"] }), ctx)).toEqual([
    "running",
  ]);
  expect(
    marksFor(card("C-5", { modifiers: ["running", "unresponsive"] }), ctx),
  ).toEqual(["unresponsive"]);
  expect(marksFor(card("C-6"), ctx)).toEqual(["approved_operation"]);
  expect(marksFor(card("C-7"), ctx)).toEqual([]);
});

it("AC-197: 60 文字ちょうどは全文、61 文字は先頭 60 文字と「…」", () => {
  const sixty = "あ".repeat(60);
  expect(faceHeadline(sixty)).toBe(sixty);
  expect(faceHeadline(`${sixty}い`)).toBe(`${sixty}…`);
});
