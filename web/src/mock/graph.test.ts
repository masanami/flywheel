import { expect, it } from "vitest";
import { MockStore, graphOf, seedLarge, waitingOf } from "./store.ts";

// 依存の後続は、先行が完了するまで着手できない。偽のデータがこの順序と矛盾しないことを固定する。
const WAITING = ["classified", "awaiting_plan_approval"];

it("偽の関係: 先行が完了していない依存の後続は、分類済か計画承認待ちに留まる", () => {
  let checked = 0;
  const all = { ...new MockStore().workspaces, ...seedLarge() };
  for (const [name, w] of Object.entries(all)) {
    const status = new Map(w.items.map((m) => [m.c.id, m.c.status]));
    // 前提の依存だけが順序を決める（衝突は同時に走らせないだけで、着手の門ではない）
    for (const d of w.graph.deps.filter((x) => x.kind === "prerequisite")) {
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
  // 取り下げは完了とは別の区分
  expect(node("C-21")?.category).toBe("withdrawn");
  expect(node("C-22")?.category).toBe("done");
  // 状態と人間の操作待ちの中身はカードの導出と同じ（線の無い TODO と、複数の操作待ちを持つ課題が見える）
  expect(node("C-23")).toMatchObject({
    state: "unclassified",
    next_human_actions: [],
  });
  expect(node("C-24")).toMatchObject({
    state: "classified",
    next_human_actions: [],
  });
  expect(node("C-7")).toMatchObject({
    state: "awaiting_completion_approval",
    next_human_actions: ["approve_completion", "approve_operation"],
  });
  // 優先度は課題の優先度そのまま（カード／一覧と同じ値）。C-25 は線が無くても前に出る P0 の分類待ち
  expect(node("C-3")?.priority).toBe("P0");
  expect(node("C-24")?.priority).toBe("P2");
  expect(node("C-25")).toMatchObject({
    state: "unclassified",
    priority: "P0",
    next_human_actions: [],
  });
  expect(node("C-5")).toMatchObject({ running: true, stale: false });
  expect(node("C-6")).toMatchObject({ running: true, stale: true });
  expect(node("C-12")?.parent_id).toBe("C-14");
  // 人間の操作待ちの親 C-3（開いた大枠の見出しにチップが出る）の子
  expect(node("C-26")).toMatchObject({
    parent_id: "C-3",
    state: "in_progress",
  });
  expect(node("C-14")?.parent_id).toBeNull();
  expect(new Set(g.edges.map((e) => e.kind))).toEqual(
    new Set(["prerequisite", "conflict"]),
  );
  expect(new Set(g.edges.map((e) => e.registered_via))).toEqual(
    new Set(["plan", "manual", "unknown", "prediction"]),
  );
  const keys = g.edges.map((e) => [e.from, e.to]);
  const sorted = [...keys].sort(
    (a, b) =>
      Number(a[0]?.slice(2)) - Number(b[0]?.slice(2)) ||
      Number(a[1]?.slice(2)) - Number(b[1]?.slice(2)),
  );
  expect(keys).toEqual(sorted);
  // 先行が完了している前提だけが満たされている。衝突は満たされていない（終わった側があれば返さない）
  for (const e of g.edges)
    expect(e.satisfied).toBe(
      e.kind === "prerequisite" && node(e.from)?.category === "done",
    );
});

it("偽の API の graph の衝突: 後回し → 先に委譲の向き・向きの無い組・終わった相手との組は返さない・前提と衝突の両方で待つ課題", () => {
  const alpha = new MockStore().workspaces.alpha;
  if (!alpha) throw new Error("alpha が無い");
  const g = graphOf(alpha);
  const conflicts = g.edges.filter((e) => e.kind === "conflict");
  const pairs = conflicts.map((e) => [e.from, e.to, e.directed]);
  expect(pairs).toEqual([
    ["C-19", "C-6", true],
    ["C-20", "C-5", true],
    ["C-20", "C-26", false],
    ["C-26", "C-6", true],
  ]);
  // 終わった（C-12 は完了、C-21 は取り下げ）課題との衝突は、行があっても返さない
  expect(
    alpha.graph.deps.filter(
      (d) => d.kind === "conflict" && (d.to === "C-12" || d.to === "C-21"),
    ),
  ).toHaveLength(2);
  expect(conflicts.some((e) => e.to === "C-12" || e.to === "C-21")).toBe(false);
  // 向きの無い組の from は ID の小さい側
  for (const e of conflicts.filter((x) => !x.directed))
    expect(Number(e.from.slice(2))).toBeLessThan(Number(e.to.slice(2)));
});

it("偽の API の waiting: 前提と衝突を別の一覧で返す。先に委譲した側・向きの無い組・終わった相手の衝突には後回しが付かない", () => {
  const alpha = new MockStore().workspaces.alpha;
  if (!alpha) throw new Error("alpha が無い");
  const ids = (xs: { challenge_id: string }[]) => xs.map((x) => x.challenge_id);
  // C-19 は前提（C-5・C-17）と衝突（C-6 に譲る）の両方で待っている
  const w19 = waitingOf(alpha, "C-19");
  expect(ids(w19.prerequisites)).toEqual(["C-5", "C-17"]);
  expect(ids(w19.conflicts)).toEqual(["C-6"]);
  expect(w19.conflicts[0]).toMatchObject({ status: "in_progress" });
  // C-20 は C-5 に譲った。向きの無い C-26 との組、取り下げの C-21 との組は理由にならない
  expect(ids(waitingOf(alpha, "C-20").conflicts)).toEqual(["C-5"]);
  expect(ids(waitingOf(alpha, "C-5").conflicts)).toEqual([]);
  // 自分が終わった課題には理由が無い
  expect(waitingOf(alpha, "C-12")).toEqual({
    conflicts: [],
    prerequisites: [],
  });
});
