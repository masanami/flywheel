// 画面のボタンで選ぶだけの回答（POST …/challenges/{id}/holds/{hold}/answer）の偽の応答。
// 本物の core の断り方（400・403・404・409）を同じ順で写す（照合の値が空 400 → 無い 404 → 閉じている 409 → 照合の値が違う 409 → 選択肢なし 403 → 識別が無い 400 → ボタンで選べない 403）。照合の値は、保留が持つ値をそのまま使う。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { MockContext } from "./context.ts";

export function answerMock(ctx: MockContext): Pick<ApiClient, "answerHold"> {
  return {
    async answerHold(name, challengeId, holdId, req) {
      await ctx.wait();
      if (req.check === "") {
        throw new ApiError("validation_failed", "照合の値が要ります", 400);
      }
      const m = ctx.itemOf(name, challengeId);
      const h = m.holds.find((x) => x.hold_id === holdId);
      if (h === undefined) {
        throw new ApiError("not_found", "その保留はありません", 404);
      }
      if (h.answer !== null || h.closed_at !== null) {
        throw new ApiError("conflict", "この保留はもう閉じています", 409);
      }
      if ((h.check ?? "") !== req.check) {
        throw new ApiError("conflict", "問いか選択肢が変わりました", 409);
      }
      if (h.options.length === 0) {
        throw new ApiError(
          "verification_rejected",
          "選択肢の無い問いは対話で答えます",
          403,
        );
      }
      const opt = h.options.find((o) => o.id === req.option);
      if (opt === undefined) {
        throw new ApiError("validation_failed", "その選択肢はありません", 400);
      }
      if (opt.button !== true) {
        throw new ApiError(
          "verification_rejected",
          "この選択肢は対話で伝えます",
          403,
        );
      }
      const now = new Date().toISOString();
      h.answer = opt.label;
      h.answer_option = opt.id;
      h.answered_at = now;
      h.answered_by = "owner";
      h.answered_channel = "ui";
      h.answered_verification = "none";
      h.closed_at = now;
      return {
        cycle: { started: true, reason: "" },
        hold: { ...h },
      };
    },
  };
}
