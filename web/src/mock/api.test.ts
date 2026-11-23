import { beforeEach, describe, expect, it } from "vitest";
import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import { CONDITIONS_AFTER_RELEASE_HEADINGS } from "../api/types.gen.ts";
import { HOLD_KIND_PARENT_DIRECT } from "../labels.ts";
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
    expect(q.questions.map((x) => x.challenge_id)).toEqual(["C-8", "C-28"]);
  });
  it("サイクルの状態は GET …/cycle と同じ形（state・last_*・running）で返る", async () => {
    const c = await api.cycle("alpha");
    expect(c.state).toBe("running");
    expect(Object.keys(c).sort()).toEqual([
      "costs",
      "last_end_reason",
      "last_ended_at",
      "last_result",
      "last_started_at",
      "running",
      "state",
      "supervisor_actions",
    ]);
    expect(Array.isArray(c.supervisor_actions)).toBe(true);
    expect(Array.isArray(c.costs)).toBe(true);
    expect((await api.cycle("beta")).state).toBe("idle");
  });
  it("読めないワークスペースは state のコードで失敗する", async () => {
    expect(await codeOf(api.status("gamma"))).toBe("store_not_found");
    expect(await codeOf(api.status("nope"))).toBe("not_found");
  });
});

describe("知らせと取り込みの偽の口", () => {
  it("知らせは alpha だけが 4 種を新しい順に返し、読めないワークスペースは失敗する", async () => {
    const { notices } = await api.notices("alpha");
    expect(notices.map((n) => n.kind)).toEqual([
      "supervisor_escalated",
      "cycle_ended",
      "released",
      "task_added",
    ]);
    expect(await api.notices("beta")).toEqual({ notices: [] });
    expect(await codeOf(api.notices("nope"))).toBe("not_found");
  });
  it("取り込みの口は、空・重複・取り込めるものでない指定を 400 で断り、課題を足さない", async () => {
    const store = new MockStore();
    const a = createMockApi(store, { latencyMs: 0 });
    const before = store.workspaces.alpha?.items.length;
    expect(await codeOf(a.ingest("alpha", { items: [] }))).toBe(
      "validation_failed",
    );
    expect(
      await codeOf(
        a.ingest("alpha", {
          items: ["example/repo#904", "example/repo#904"],
        }),
      ),
    ).toBe("validation_failed");
    expect(await codeOf(a.ingest("alpha", { items: ["nope#1"] }))).toBe(
      "validation_failed",
    );
    expect(store.workspaces.alpha?.items.length).toBe(before);
  });
  it("取り込みが成立すると、課題を 1 つ足し、子の数を返し、取り込めるものの一覧から外す", async () => {
    const store = new MockStore();
    const a = createMockApi(store, { latencyMs: 0 });
    const res = await a.ingest("alpha", { items: ["example/repo#901"] });
    expect(res.ingested).toHaveLength(1);
    expect(res.ingested[0]?.children).toBe(2);
    const added = store.workspaces.alpha?.items.some(
      (m) => m.c.id === res.ingested[0]?.challenge_id,
    );
    expect(added).toBe(true);
    const left = (await a.statusIngestable("alpha")).ingestable?.items ?? [];
    expect(left.some((it) => it.external_key === "example/repo#901")).toBe(
      false,
    );
  });
});

describe("ボタンの回答の偽の口", () => {
  it("照合の値が空は 400、無い保留は 404、選択肢の無い保留は 403", async () => {
    const store = new MockStore();
    const a = createMockApi(store, { latencyMs: 0 });
    const h = store.workspaces.alpha?.items.find((x) => x.c.id === "C-8")
      ?.holds[0];
    if (!h) throw new Error("種の保留が無い");
    h.check = "c";
    h.options = [];
    expect(
      await codeOf(
        a.answerHold("alpha", "C-8", "H-1", { check: "", option: "o" }),
      ),
    ).toBe("validation_failed");
    expect(
      await codeOf(
        a.answerHold("alpha", "C-8", "H-99", { check: "c", option: "o" }),
      ),
    ).toBe("not_found");
    expect(
      await codeOf(
        a.answerHold("alpha", "C-8", "H-1", { check: "c", option: "o" }),
      ),
    ).toBe("verification_rejected");
  });
  it("照合の値が違えば 409、合えば経路 ui の回答として記録し、閉じた保留への回答は 409", async () => {
    const store = new MockStore();
    const a = createMockApi(store, { latencyMs: 0 });
    const m = store.workspaces.alpha?.items.find((x) => x.c.id === "C-8");
    const h = m?.holds[0];
    if (!h) throw new Error("種の保留が無い");
    h.options = [
      { id: "x", label: "選ぶ", description: "", button: true },
      { id: "y", label: "対話で", description: "", button: false },
    ];
    h.check = "now";
    expect(
      await codeOf(
        a.answerHold("alpha", "C-8", h.hold_id, { check: "old", option: "x" }),
      ),
    ).toBe("conflict");
    expect(
      await codeOf(
        a.answerHold("alpha", "C-8", h.hold_id, { check: "now", option: "y" }),
      ),
    ).toBe("verification_rejected");
    expect(
      await codeOf(
        a.answerHold("alpha", "C-8", h.hold_id, { check: "now", option: "zz" }),
      ),
    ).toBe("validation_failed");
    const r = await a.answerHold("alpha", "C-8", h.hold_id, {
      check: "now",
      option: "x",
    });
    expect(r.cycle.started).toBe(true);
    expect(h.answered_channel).toBe("ui");
    expect(h.answered_verification).toBe("none");
    expect(h.answer).toBe("選ぶ");
    expect(
      await codeOf(
        a.answerHold("alpha", "C-8", h.hold_id, { check: "now", option: "x" }),
      ),
    ).toBe("conflict");
  });
});

describe("承認（偽の ① ②）", () => {
  it("完了確認待ちの ① は release の head を返し、② は版が違えば conflict、合えば進める", async () => {
    const s = await api.approvalSummary("alpha", "C-7");
    expect(s.approval.kind).toBe("completion");
    expect(s.approval.release_held).toBe(true);
    expect(s.approval.conditions_after_release).toEqual([
      "本番の料金表の画面で、請求書と同じ金額が出る",
    ]);
    expect(s.approval.conditions_after_release_heading).toBe(
      CONDITIONS_AFTER_RELEASE_HEADINGS[0],
    );
    expect(s.approval.conditions_after_release_heading).toContain(
      "満たされないまま残る",
    );
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
    for (const h of m.holds) h.kind = HOLD_KIND_PARENT_DIRECT;
    return { w, m };
  }

  it("status は parent_agent に出して needs_human から外し、カードは問いに答える操作を出さない", async () => {
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
    // オーナーが答える問いにも出ない（C-28 のタスクの問いは親が直接行う保留ではないので残る）
    expect((await a.questions()).questions.map((x) => x.challenge_id)).toEqual([
      "C-28",
    ]);
  });

  it("種類が人の対応を待つ保留の課題は、従来どおり needs_human と問いに出る", async () => {
    const status = await api.status("alpha");
    expect(status.parent_agent.challenges).toEqual([]);
    expect(status.needs_human.challenges.map((c) => c.id)).toContain("C-8");
  });

  it("承認の ① は、直近が直接行った完了（T17）で完了確認待ちに入った課題にだけ、成果物の参照を完了条件と並べて返す", async () => {
    const store = new MockStore();
    const m = store.workspaces.alpha?.items.find((i) => i.c.id === "C-7");
    if (!m) throw new Error("fixture");
    m.directCompletions.push({
      artifacts: ["https://example.com/result"],
      created_at: "2026-10-12T09:00:00Z",
      note: null,
    });
    m.completionDirect = true;
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

  it("記録があっても、直近が直接行った完了でなければ（検証の met で入り直した）承認の ① に出さない", async () => {
    const store = new MockStore();
    const m = store.workspaces.alpha?.items.find((i) => i.c.id === "C-7");
    if (!m) throw new Error("fixture");
    m.directCompletions.push({
      artifacts: ["https://example.com/old"],
      created_at: "2026-10-12T09:00:00Z",
      note: null,
    });
    m.completionDirect = false;
    const a = createMockApi(store, { latencyMs: 0 });
    expect(
      (await a.approvalSummary("alpha", "C-7")).approval.direct_completion,
    ).toBeNull();
    // 詳細には記録が残る
    expect((await a.challenge("alpha", "C-7")).direct_completions).toHaveLength(
      1,
    );
  });

  it("完了確認待ちを承認で出ると、直接行った完了の印は消える", async () => {
    const store = new MockStore();
    const m = store.workspaces.alpha?.items.find((i) => i.c.id === "C-7");
    if (!m) throw new Error("fixture");
    m.directCompletions.push({
      artifacts: ["https://example.com/result"],
      created_at: "2026-10-12T09:00:00Z",
      note: null,
    });
    m.completionDirect = true;
    const a = createMockApi(store, { latencyMs: 0 });
    await a.approve("alpha", "C-7", m.c.version);
    expect(m.c.status).toBe("done");
    expect(m.completionDirect).toBe(false);
  });
  it("承認済みで未実行の release があるときの ① の見出しは「満たされないまま残る」ではない", async () => {
    const store = new MockStore();
    const item = store.workspaces.alpha?.items.find((i) => i.c.id === "C-7");
    for (const o of item?.operations ?? []) {
      if (o.kind === "release") o.state = "approved";
    }
    const s = await createMockApi(store, { latencyMs: 0 }).approvalSummary(
      "alpha",
      "C-7",
    );
    expect(s.approval.conditions_after_release_heading).toBe(
      CONDITIONS_AFTER_RELEASE_HEADINGS[2],
    );
    expect(s.approval.conditions_after_release_heading).not.toContain(
      "満たされないまま残る",
    );
  });
});

describe("フォローアップ（偽のデータ）", () => {
  it("AC-422 #812: alpha は、未・済み・捨てたのフォローアップを 1 件以上ずつ持つ", async () => {
    const all = await api.followups("alpha", "all");
    for (const s of ["open", "done", "dropped"]) {
      expect(
        all.followups.filter((f) => f.status === s).length,
      ).toBeGreaterThan(0);
    }
    const open = await api.followups("alpha");
    expect(open.followups).toHaveLength(3);
    expect(open.followups.every((f) => f.status === "open")).toBe(true);
  });
});
