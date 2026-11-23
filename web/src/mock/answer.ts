// 画面のボタンで選ぶだけの回答（POST …/challenges/{id}/holds/{hold}/answer）の偽の応答。口は置いたが、中身は後続の課題が埋める。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { MockContext } from "./context.ts";

export function answerMock(ctx: MockContext): Pick<ApiClient, "answerHold"> {
  return {
    async answerHold() {
      await ctx.wait();
      throw new ApiError("not_found", "偽の API はまだこの口を持たない", 404);
    },
  };
}
