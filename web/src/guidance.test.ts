import { expect, it } from "vitest";
import {
  type Template,
  guidesFor,
  guidesForContext,
  shellQuote,
} from "./guidance.ts";

// 案内の「出す場面の書き方」を、課題の操作に結び付かない案内（context）へ広げた形の確認。#803
const templates: Template[] = [
  {
    id: "by-action",
    when: { actions: ["approve_plan"] },
    template: "flywheel reject <C-ID> --workspace <パス> --reason <理由>",
  },
  {
    id: "by-context",
    when: { context: "followups" },
    template: "flywheel followup done <F-ID> --workspace <パス> --note <メモ>",
    note: "済みにします",
  },
];

it("#803: context の案内は、その場面だけで出て、パスを引用して差し込む", () => {
  expect(guidesForContext("followups", "/w/a b", templates)).toEqual([
    {
      id: "by-context",
      command:
        "flywheel followup done <F-ID> --workspace '/w/a b' --note <メモ>",
      note: "済みにします",
    },
  ]);
  expect(guidesForContext("ingest", "/w", templates)).toEqual([]);
});

it("#803: 課題の操作の案内は、これまでどおり actions で出て、context の案内は混ざらない", () => {
  const ids = guidesFor("C-1", "/w", ["approve_plan"]).map((g) => g.id);
  expect(ids).toContain("reject");
  expect(ids).not.toContain("by-context");
});

it("answer・budget の案内は差し込み口 <回答>・<額> のまま、コードで決まる", () => {
  const a = guidesFor("C-1", "/w/a", ["answer_question"]);
  expect(a.map((g) => g.command)).toEqual([
    "flywheel answer C-1 --workspace '/w/a' --answer <回答>",
  ]);
  const b = guidesFor("C-1", "/w/a", ["increase_budget"]);
  expect(b.map((g) => g.command)).toEqual([
    "flywheel budget C-1 --workspace '/w/a' --impl-usd <額>",
  ]);
  expect(guidesFor("C-1", "/w/a", [])).toEqual([]);
});

it("<パス> は単一引用符で囲み、中の単一引用符は '\\'' にする", () => {
  expect(shellQuote("/a b/c")).toBe("'/a b/c'");
  expect(shellQuote('/it\'s "x"')).toBe("'/it'\\''s \"x\"'");
  const [d] = guidesFor("C-1", "/tmp/a$$b$&", ["approve_completion"]);
  expect(d?.command).toBe(
    "flywheel reject C-1 --workspace '/tmp/a$$b$&' --reason <理由>",
  );
});
