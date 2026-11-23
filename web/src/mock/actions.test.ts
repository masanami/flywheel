import { beforeEach, describe, expect, it } from "vitest";
import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
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

describe("サイクルを止める・フォローアップを済みにする／捨てる（偽）", () => {
  it("走っているサイクルを止めると、止める指示が出た旨が GET …/cycle に出る", async () => {
    expect((await api.cycle("alpha")).running?.stop_requested).toBe(false);
    expect(await api.cycleStop("alpha")).toMatchObject({ requested: true });
    expect((await api.cycle("alpha")).running?.stop_requested).toBe(true);
  });
  it("済みにする・捨てるは、1 つでも未でなければどれも変えない", async () => {
    const before = structuredClone(await api.followups("alpha", "all"));
    expect(
      await codeOf(
        api.followupsDone("alpha", { ids: ["F-1", "F-4"], note: "n" }),
      ),
    ).toBe("invalid_transition");
    expect(await api.followups("alpha", "all")).toEqual(before);
    await api.followupsDone("alpha", { ids: ["F-1"], note: "確かめた" });
    await api.followupsDrop("alpha", { ids: ["F-2"], reason: "不要" });
    const open = await api.followups("alpha");
    expect(open.followups.map((f) => f.id)).toEqual(["F-3"]);
  });
});
