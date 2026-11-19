import { expect, it } from "vitest";
import { type Template, guidesFor, guidesForContext } from "./guidance.ts";

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
