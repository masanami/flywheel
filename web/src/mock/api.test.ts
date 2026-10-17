import { beforeEach, describe, expect, it } from "vitest";
import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/proposed.ts";
import { createMockApi } from "./api.ts";
import { MockStore } from "./store.ts";

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
  it("列の見出しの材料は、サイクルの状態と方針のモードだけを返す（差し込みは持たない）", async () => {
    const info = await api.cycleInfo("alpha");
    expect(info.cycle_state).toBe("running");
    expect(info.policy_mode).toContain("優先度方針");
    expect(Object.keys(info).sort()).toEqual([
      "cycle_state",
      "last_cycle_at",
      "policy_mode",
    ]);
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
