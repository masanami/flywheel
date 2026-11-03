import { expect, it } from "vitest";
import { MockStore, graphOf } from "./store.ts";

// 依存の後続は、先行が完了するまで着手できない。偽のデータがこの順序と矛盾しないことを固定する。
const WAITING = ["classified", "awaiting_plan_approval"];

it("偽の関係: 先行が完了していない依存の後続は、分類済か計画承認待ちに留まる", () => {
  let checked = 0;
  for (const [name, w] of Object.entries(new MockStore().workspaces)) {
    const status = new Map(w.items.map((m) => [m.c.id, m.c.status]));
    for (const d of w.graph.deps) {
      const from = status.get(d.from);
      const to = status.get(d.to);
      expect(from, `${name}: ${d.from} が無い`).toBeDefined();
      expect(to, `${name}: ${d.to} が無い`).toBeDefined();
      checked++;
      if (from === "done") continue;
      expect(
        WAITING,
        `${name}: ${d.from}（${from}）→ ${d.to}（${to}）`,
      ).toContain(to);
    }
  }
  expect(checked).toBeGreaterThan(0);
});

it("偽の API の graph: ID の数の昇順・区分と実施中と更新なしが課題の導出と合う・親子と依存の種類と経路が揃う", () => {
  const alpha = new MockStore().workspaces.alpha;
  if (!alpha) throw new Error("alpha が無い");
  const g = graphOf(alpha);
  expect(g.nodes.map((n) => n.id)).toEqual(
    Array.from({ length: g.nodes.length }, (_, i) => `C-${i + 1}`),
  );
  const node = (id: string) => g.nodes.find((n) => n.id === id);
  // 計画承認待ち = 人間の操作待ち、完了 = 完了、それ以外 = システム
  expect(node("C-3")?.category).toBe("human");
  expect(node("C-12")?.category).toBe("done");
  expect(node("C-1")?.category).toBe("system");
  expect(node("C-5")).toMatchObject({ running: true, stale: false });
  expect(node("C-6")).toMatchObject({ running: true, stale: true });
  expect(node("C-12")?.parent_id).toBe("C-14");
  expect(node("C-14")?.parent_id).toBeNull();
  expect(new Set(g.edges.map((e) => e.kind))).toEqual(
    new Set(["prerequisite", "conflict"]),
  );
  expect(new Set(g.edges.map((e) => e.registered_via))).toEqual(
    new Set(["plan", "manual", "unknown"]),
  );
  const keys = g.edges.map((e) => [e.from, e.to]);
  const sorted = [...keys].sort(
    (a, b) =>
      Number(a[0]?.slice(2)) - Number(b[0]?.slice(2)) ||
      Number(a[1]?.slice(2)) - Number(b[1]?.slice(2)),
  );
  expect(keys).toEqual(sorted);
  // 先行が完了している依存だけが満たされている
  for (const e of g.edges)
    expect(e.satisfied).toBe(node(e.from)?.category === "done");
});
