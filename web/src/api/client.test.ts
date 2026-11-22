import { afterEach, describe, expect, it, vi } from "vitest";
import { post } from "./client.ts";
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
