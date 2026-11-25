import { expect, it } from "vitest";
import { createMockApi } from "./api.ts";
import { MOCK_PERMISSION_WORD, MOCK_QUESTION_WORD } from "./chat.ts";
import { MockStore } from "./store.ts";

const mock = () => createMockApi(new MockStore(), { latencyMs: 0 });
const lastPrompt = async (api: ReturnType<typeof mock>) => {
  const p = (await api.chat("alpha")).messages.at(-1)?.prompts[0];
  if (!p) throw new Error("確認がありません");
  return p;
};
const status = (s: number, code?: string) =>
  expect.objectContaining({ status: s, ...(code ? { code } : {}) });

it("確認への答えは、無い識別 404 → 形の誤り 400 → 通る → 2 回目 409", async () => {
  const api = mock();
  await api.chatSend("alpha", { text: MOCK_PERMISSION_WORD });
  expect((await api.chat("alpha")).state).toBe("replying");
  const p = await lastPrompt(api);
  await expect(
    api.chatAnswer("alpha", 9999, { decision: "allow" }),
  ).rejects.toEqual(status(404));
  await expect(
    api.chatAnswer("alpha", p.id, { answers: { x: "y" } }),
  ).rejects.toEqual(status(400));
  await api.chatAnswer("alpha", p.id, { decision: "allow" });
  expect((await lastPrompt(api)).state).toBe("allowed");
  expect((await api.chat("alpha")).state).toBe("idle");
  await expect(
    api.chatAnswer("alpha", p.id, { decision: "allow" }),
  ).rejects.toEqual(status(409));
});

it("「止める」は、進行中の返事を止め、答えを待つ確認を expired にする", async () => {
  const api = mock();
  await api.chatSend("alpha", { text: MOCK_QUESTION_WORD });
  await api.chatStop("alpha");
  const m = (await api.chat("alpha")).messages.at(-1);
  expect(m?.state).toBe("stopped");
  expect(m?.prompts[0]?.state).toBe("expired");
  expect(m?.activity[0]?.state).toBe("error");
});

it("会話の一覧は新しい順。続けると開く会話が入れ替わり、断るときは何も変えない", async () => {
  const api = mock();
  await api.chatSend("alpha", { text: "こんにちは" });
  const { sessions } = await api.chatSessions("alpha");
  expect(sessions.map((s) => s.open)).toEqual([true, false, false]);
  await expect(api.chatResume("alpha", "nothing")).rejects.toEqual(status(404));
  await expect(api.chatResume("alpha", "mock-alpha-gone")).rejects.toEqual(
    status(409, "transcript_unavailable"),
  );
  expect((await api.chat("alpha")).session_id).toBe(sessions[0]?.id);
  await api.chatResume("alpha", "mock-alpha-spec");
  expect((await api.chat("alpha")).session_id).toBe("mock-alpha-spec");
  expect(
    (await api.chatSessions("alpha")).sessions.filter((s) => s.open),
  ).toHaveLength(1);
});

it("返事が進行中の続きは 409。開いている会話の続きは、進行中でも何もせず成功", async () => {
  const api = mock();
  await api.chatSend("alpha", { text: MOCK_PERMISSION_WORD });
  const open = (await api.chat("alpha")).session_id as string;
  await expect(api.chatResume("alpha", "mock-alpha-spec")).rejects.toEqual(
    status(409, "conflict"),
  );
  await expect(api.chatResume("alpha", open)).resolves.toEqual({});
});

it("全文は、無い識別と、ファイルの無い会話を、別のエラーコードで断る", async () => {
  const api = mock();
  await expect(api.chatTranscript("alpha", "nothing")).rejects.toEqual(
    status(404, "not_found"),
  );
  await expect(api.chatTranscript("alpha", "mock-alpha-gone")).rejects.toEqual(
    status(404, "transcript_unavailable"),
  );
  expect(
    (await api.chatTranscript("alpha", "mock-alpha-spec")).entries.length,
  ).toBeGreaterThan(0);
});

it("課題の詳細は、紐づいた会話と仕様づくりの費用を持つ", async () => {
  const { challenge } = await mock().challenge("alpha", "C-3");
  expect(challenge.spec_cost_usd).toBe(0.42);
  expect(challenge.chat_sessions.map((s) => s.id)).toEqual(["mock-alpha-spec"]);
});
