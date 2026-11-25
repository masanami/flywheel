import { afterEach, describe, expect, it, vi } from "vitest";
import { api, post, resetApi } from "./client.ts";
import { ApiError } from "./error.ts";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("post", () => {
  it("204・中身なしの成功は、エラーにせず undefined を返す", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 204 })),
    );
    await expect(post<unknown>("/api/v1/x/chat/stop")).resolves.toBeUndefined();
  });

  it("中身のある成功は、これまでどおり JSON を読む", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ id: 3 }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );
    await expect(
      post<{ id: number }>("/api/v1/x/chat/messages"),
    ).resolves.toEqual({
      id: 3,
    });
  });

  it("失敗の応答は ApiError にする", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({ error: { code: "conflict", message: "進行中" } }),
            { status: 409 },
          ),
      ),
    );
    const err = post<unknown>("/api/v1/x/chat/new");
    await expect(err).rejects.toBeInstanceOf(ApiError);
    await expect(err).rejects.toMatchObject({
      code: "conflict",
      status: 409,
    });
  });
});

// 画面のボタンで受ける操作（区切り S4d-2）の口。URL・本文・CSRF の値の載せ方を固定する。
describe("画面のボタンの口", () => {
  afterEach(() => {
    resetApi();
    document.head.innerHTML = "";
  });

  async function called(run: () => Promise<unknown>) {
    document.head.innerHTML = '<meta name="flywheel-csrf" content="tok">';
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({}), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await run();
    const [url, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    return { url, init };
  }

  it("回答は保留の ID で指し、選択肢の識別と読んだときの照合の値を送る", async () => {
    const { url, init } = await called(() =>
      api.answerHold("a b", "C-1", "H-2", { option: "o1", check: "abc" }),
    );
    expect(url).toBe(
      "/api/v1/workspaces/a%20b/challenges/C-1/holds/H-2/answer",
    );
    expect(init.method).toBe("POST");
    expect(init.body).toBe(JSON.stringify({ option: "o1", check: "abc" }));
    expect((init.headers as Record<string, string>)["X-Flywheel-CSRF"]).toBe(
      "tok",
    );
  });

  it.each([
    [
      "取り込み",
      () => api.ingest("w", { items: ["1"] }),
      "/ingest",
      { items: ["1"] },
    ],
    ["サイクルを止める", () => api.cycleStop("w"), "/cycle/stop", undefined],
    [
      "許可の確認への答え",
      () => api.chatAnswer("w", 7, { decision: "allow" }),
      "/chat/prompts/7/answer",
      { decision: "allow" },
    ],
    [
      "閉じた会話の続き",
      () => api.chatResume("w", "s/1"),
      "/chat/sessions/s%2F1/resume",
      undefined,
    ],
    [
      "済みにする",
      () => api.followupsDone("w", { ids: ["F-1"], note: "確かめた" }),
      "/followups/done",
      { ids: ["F-1"], note: "確かめた" },
    ],
    [
      "捨てる",
      () => api.followupsDrop("w", { ids: ["F-1"], reason: "不要" }),
      "/followups/drop",
      { ids: ["F-1"], reason: "不要" },
    ],
  ])("%s は CSRF つきの POST", async (_n, run, path, body) => {
    const { url, init } = await called(run);
    expect(url).toBe(`/api/v1/workspaces/w${path}`);
    expect(init.method).toBe("POST");
    expect(init.body).toBe(
      body === undefined ? undefined : JSON.stringify(body),
    );
    expect((init.headers as Record<string, string>)["X-Flywheel-CSRF"]).toBe(
      "tok",
    );
  });

  it.each([
    ["会話の一覧", () => api.chatSessions("w"), "/chat/sessions"],
    [
      "会話の全文",
      () => api.chatTranscript("w", "s/1"),
      "/chat/sessions/s%2F1/transcript",
    ],
  ])("%s は GET", async (_n, run, path) => {
    const { url, init } = await called(run);
    expect(url).toBe(`/api/v1/workspaces/w${path}`);
    expect(init.method).toBe("GET");
  });

  it("知らせは GET", async () => {
    const { url, init } = await called(() => api.notices("w"));
    expect(url).toBe("/api/v1/workspaces/w/notices");
    expect(init.method).toBe("GET");
  });
});
