import { beforeEach, describe, expect, it } from "vitest";
import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/write.ts";
import { createMockApi } from "./api.ts";
import { MockStore } from "./store.ts";

let store: MockStore;
let api: ApiClient;
beforeEach(() => {
  store = new MockStore();
  api = createMockApi(store, { latencyMs: 0 });
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
  it("読めないワークスペースは state のコードで失敗する", async () => {
    expect(await codeOf(api.status("gamma"))).toBe("store_not_found");
    expect(await codeOf(api.status("nope"))).toBe("not_found");
  });
});

describe("S2 の書き込み", () => {
  it("起票は未分類・版 1 で始まり、タイトルが空なら validation_failed", async () => {
    const r = await api.createChallenge("alpha", {
      title: "新しい課題",
      description: "",
      done_criteria: "",
      urgency: "高",
    });
    expect(r.challenge).toMatchObject({ status: "unclassified", version: 1 });
    expect(
      await codeOf(
        api.createChallenge("alpha", {
          title: " ",
          description: "",
          done_criteria: "",
          urgency: null,
        }),
      ),
    ).toBe("validation_failed");
  });
  it("編集は版が違えば conflict で、変更しない", async () => {
    const before = (await api.challenge("alpha", "C-2")).challenge;
    const input = {
      version: before.version,
      title: "直した",
      description: before.description,
      done_criteria: before.done_criteria,
      urgency: before.urgency,
    };
    const c2 = store.workspaces.alpha?.items.find((i) => i.c.id === "C-2");
    if (c2) c2.c.version += 1;
    expect(await codeOf(api.editChallenge("alpha", "C-2", input))).toBe(
      "conflict",
    );
    expect((await api.challenge("alpha", "C-2")).challenge.title).toBe(
      before.title,
    );
  });
  it("優先度の変更は分類済み以降で完了前だけ。状態は変えない", async () => {
    const c2 = (await api.challenge("alpha", "C-2")).challenge;
    const r = await api.prioritize("alpha", "C-2", {
      version: c2.version,
      priority: "P0",
    });
    expect(r.challenge).toMatchObject({ priority: "P0", status: "classified" });
    expect(
      await codeOf(
        api.prioritize("alpha", "C-1", { version: 1, priority: "P0" }),
      ),
    ).toBe("validation_failed");
    expect(
      await codeOf(
        api.prioritize("alpha", "C-12", { version: 6, priority: "P0" }),
      ),
    ).toBe("validation_failed");
  });
});

describe("S3 のまとめての承認", () => {
  it("計画の承認と完了の承認を 1 回で行い、完了の承認で release も承認される", async () => {
    const p = await api.prepareApproval("alpha", [
      { kind: "challenge", id: "C-3" },
      { kind: "challenge", id: "C-7" },
    ]);
    expect(p.items[1]?.simultaneous_operation_ids).toEqual(["O-1"]);
    const r = await api.executeApproval("alpha", {
      challenge_id: p.challenge_id,
      items: p.items.map((i) => ({
        id: i.id,
        version: i.version,
        hold_release: i.hold_release,
      })),
      attestation: "mock-passkey",
    });
    expect(r.approved).toEqual(["C-3", "C-7"]);
    const s = await api.status("alpha");
    expect(s.approved.operations.map((o) => o.id)).toEqual(["O-1"]);
    expect((await api.challenge("alpha", "C-7")).challenge.status).toBe("done");
    expect((await api.challenge("alpha", "C-3")).challenge.status).toBe(
      "in_progress",
    );
  });
  it("集合の構成の規則: 重複・空・完了の承認と同じ課題の release は validation_failed", async () => {
    expect(await codeOf(api.prepareApproval("alpha", []))).toBe(
      "validation_failed",
    );
    expect(
      await codeOf(
        api.prepareApproval("alpha", [
          { kind: "challenge", id: "C-3" },
          { kind: "challenge", id: "C-3" },
        ]),
      ),
    ).toBe("validation_failed");
    expect(
      await codeOf(
        api.prepareApproval("alpha", [
          { kind: "challenge", id: "C-7" },
          { kind: "operation", id: "O-1" },
        ]),
      ),
    ).toBe("validation_failed");
  });
  it("確認後に 1 件でも版が変われば全体を conflict で拒否し、何も承認しない", async () => {
    const p = await api.prepareApproval("alpha", [
      { kind: "challenge", id: "C-3" },
      { kind: "challenge", id: "C-4" },
    ]);
    const c4 = store.workspaces.alpha?.items.find((i) => i.c.id === "C-4");
    if (c4) c4.c.version += 1;
    const code = await codeOf(
      api.executeApproval("alpha", {
        challenge_id: p.challenge_id,
        items: p.items.map((i) => ({
          id: i.id,
          version: i.version,
          hold_release: false,
        })),
        attestation: "mock-passkey",
      }),
    );
    expect(code).toBe("conflict");
    expect((await api.challenge("alpha", "C-3")).challenge.status).toBe(
      "awaiting_plan_approval",
    );
  });
  it("署名の対象は 1 回だけ使える", async () => {
    const p = await api.prepareApproval("beta", [
      { kind: "challenge", id: "C-2" },
    ]);
    const input = {
      challenge_id: p.challenge_id,
      items: p.items.map((i) => ({
        id: i.id,
        version: i.version,
        hold_release: false,
      })),
      attestation: "mock-passkey" as const,
    };
    await api.executeApproval("beta", input);
    expect(await codeOf(api.executeApproval("beta", input))).toBe(
      "verification_rejected",
    );
  });
});

describe("S3 の差し戻し・回答・増額（1 件ずつ）", () => {
  const exec = async (req: Parameters<ApiClient["prepareDecision"]>[1]) => {
    const p = await api.prepareDecision("alpha", req);
    return api.executeDecision("alpha", {
      sign_id: p.sign_id,
      version: p.version,
      attestation: "mock-passkey",
    });
  };
  it("差し戻しは理由が要り、計画承認待ちは分類済みへ戻る", async () => {
    expect(
      await codeOf(
        api.prepareDecision("alpha", {
          action: "reject",
          challenge_id: "C-3",
          reason: "",
        }),
      ),
    ).toBe("validation_failed");
    await exec({ action: "reject", challenge_id: "C-3", reason: "範囲が広い" });
    expect((await api.challenge("alpha", "C-3")).challenge.status).toBe(
      "classified",
    );
  });
  it("回答は保留に入る前の状態へ戻し、問いの一覧から消える", async () => {
    await exec({ action: "answer", challenge_id: "C-8", answer: "含めない" });
    expect((await api.challenge("alpha", "C-8")).challenge.status).toBe(
      "in_progress",
    );
    expect((await api.questions()).questions).toEqual([]);
  });
  it("増額は使い切った課題だけで、額は 0 超 100 以下", async () => {
    expect(
      await codeOf(
        api.prepareDecision("alpha", {
          action: "increase_budget",
          challenge_id: "C-9",
          amount_usd: 0,
        }),
      ),
    ).toBe("validation_failed");
    await exec({
      action: "increase_budget",
      challenge_id: "C-9",
      amount_usd: 5,
    });
    expect((await api.status("alpha")).needs_human.budget_exhausted).toEqual(
      [],
    );
  });
  it("確認後に版が変われば conflict", async () => {
    const p = await api.prepareDecision("alpha", {
      action: "answer",
      challenge_id: "C-8",
      answer: "はい",
    });
    const c8 = store.workspaces.alpha?.items.find((i) => i.c.id === "C-8");
    if (c8) c8.c.version += 1;
    expect(
      await codeOf(
        api.executeDecision("alpha", {
          sign_id: p.sign_id,
          version: p.version,
          attestation: "mock-passkey",
        }),
      ),
    ).toBe("conflict");
  });
});
