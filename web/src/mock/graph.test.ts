import { expect, it } from "vitest";
import { seed } from "./store.ts";

// 依存の後続は、先行が完了するまで着手できない。偽のデータがこの順序と矛盾しないことを固定する。
const WAITING = ["classified", "awaiting_plan_approval"];

it("偽の関係: 先行が完了していない依存の後続は、分類済か計画承認待ちに留まる", () => {
  let checked = 0;
  for (const [name, w] of Object.entries(seed())) {
    const status = new Map(w.items.map((m) => [m.c.id, m.c.status]));
    for (const r of w.graph.relations) {
      if (r.kind !== "depends_on") continue;
      const from = status.get(r.from);
      const to = status.get(r.to);
      expect(from, `${name}: ${r.from} が無い`).toBeDefined();
      expect(to, `${name}: ${r.to} が無い`).toBeDefined();
      checked++;
      if (from === "done") continue;
      expect(
        WAITING,
        `${name}: ${r.from}（${from}）→ ${r.to}（${to}）`,
      ).toContain(to);
    }
  }
  expect(checked).toBeGreaterThan(0);
});
