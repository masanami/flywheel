import { beforeEach, describe, expect, it } from "vitest";
import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import { createMockApi } from "./api.ts";
import { MockStore, graphOf } from "./store.ts";

let api: ApiClient;
beforeEach(() => {
  api = createMockApi(new MockStore(), { latencyMs: 0 });
});

const codeOf = async (p: Promise<unknown>) => {
  try {
    await p;
  } catch (e) {
    if (e instanceof ApiError) return e.code;
    throw e;
  }
  return "ok";
};

describe("読み取り（types.gen.ts の形）", () => {
  it("一覧・状況・needs-human・問いが整合する", async () => {
    const list = await api.challenges("alpha");
    const status = await api.status("alpha");
    const nh = await api.needsHuman();
    const waiting = list.challenges.filter(
      (c) => c.card.next_human_actions.length > 0,
    );
    expect(waiting.length).toBeGreaterThan(0);
    expect(nh.workspaces.map((w) => w.name)).toEqual(["alpha", "beta"]);
    expect(status.needs_human.challenges.map((c) => c.id)).toContain("C-3");
    const q = await api.questions();
    expect(q.questions.map((x) => x.challenge_id)).toEqual(["C-8"]);
  });
  it("サイクルの状態は GET …/cycle と同じ形（state・last_*）で返る", async () => {
    const c = await api.cycle("alpha");
    expect(c.state).toBe("running");
    expect(Object.keys(c).sort()).toEqual([
      "last_ended_at",
      "last_result",
      "last_started_at",
      "state",
    ]);
    expect((await api.cycle("beta")).state).toBe("idle");
  });
  it("読めないワークスペースは state のコードで失敗する", async () => {
    expect(await codeOf(api.status("gamma"))).toBe("store_not_found");
    expect(await codeOf(api.status("nope"))).toBe("not_found");
  });
});

describe("承認（偽の ① ②）", () => {
  it("完了確認待ちの ① は release の head を返し、② は版が違えば conflict、合えば進める", async () => {
    const s = await api.approvalSummary("alpha", "C-7");
    expect(s.approval.kind).toBe("completion");
    expect(s.approval.release_held).toBe(true);
    expect(
      await codeOf(api.approve("alpha", "C-7", s.approval.version - 1)),
    ).toBe("conflict");
    const r = await api.approve("alpha", "C-7", s.approval.version);
    expect(r.challenge.status).toBe("done");
  });
});

describe("親が直接行う保留（#452）", () => {
  // 既存の課題 C-8（人間対応待ち）の開いている保留を、種類が親が直接行うにする
  function makeParentDirect(store: MockStore) {
    const w = store.workspaces.alpha;
    const m = w?.items.find((i) => i.c.id === "C-8");
    if (!w || !m) throw new Error("fixture");
    for (const h of m.holds) h.kind = "parent_direct";
    return { w, m };
  }

  it("status は parent_agent に出して needs_human から外し、カードは問いに答える操作を出さず、グラフの区分は parent_agent", async () => {
    const store = new MockStore();
    const { w } = makeParentDirect(store);
    const a = createMockApi(store, { latencyMs: 0 });
    const status = await a.status("alpha");
    expect(status.parent_agent.challenges.map((c) => c.id)).toEqual(["C-8"]);
    expect(status.parent_agent.challenges[0]?.status).toBe("awaiting_human");
    expect(status.needs_human.challenges.map((c) => c.id)).not.toContain("C-8");
    const card = (await a.challenges("alpha")).challenges.find(
      (c) => c.id === "C-8",
    );
    expect(card?.card.state).toBe("awaiting_human");
    expect(card?.card.next_human_actions).toEqual([]);
    const node = graphOf(w).nodes.find((n) => n.id === "C-8");
    expect(node?.category).toBe("parent_agent");
    // 終わった課題・関係を持たない課題は従来どおり
    const cats = new Map(graphOf(w).nodes.map((n) => [n.id, n.category]));
    expect(cats.get("C-12")).toBe("done");
    expect(cats.get("C-21")).toBe("withdrawn");
    // オーナーが答える問いにも出ない
    expect((await a.questions()).questions.map((x) => x.challenge_id)).toEqual(
      [],
    );
  });

  it("種類が人の対応を待つ保留の課題は、従来どおり needs_human と問いに出る", async () => {
    const status = await api.status("alpha");
    expect(status.parent_agent.challenges).toEqual([]);
    expect(status.needs_human.challenges.map((c) => c.id)).toContain("C-8");
  });

  it("承認の ① は、直接行った記録の成果物の参照を完了条件と並べて返す", async () => {
    const store = new MockStore();
    const m = store.workspaces.alpha?.items.find((i) => i.c.id === "C-7");
    if (!m) throw new Error("fixture");
    m.directCompletions.push({
      artifacts: ["https://example.com/result"],
      created_at: "2026-10-12T09:00:00Z",
      note: null,
    });
    const a = createMockApi(store, { latencyMs: 0 });
    const s = await a.approvalSummary("alpha", "C-7");
    expect(s.approval.direct_completion?.artifacts).toEqual([
      "https://example.com/result",
    ]);
    expect(s.approval.done_criteria).not.toBeNull();
    expect((await a.challenge("alpha", "C-7")).direct_completions).toHaveLength(
      1,
    );
    // 記録の無い課題は null / 空
    expect(
      (await api.approvalSummary("alpha", "C-7")).approval.direct_completion,
    ).toBeNull();
  });
});
