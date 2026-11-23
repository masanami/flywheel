// 画面のボタンでの取り込み（POST …/ingest）の偽の応答。口は置いたが、中身は後続の課題が埋める。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { MockContext } from "./context.ts";

export function ingestMock(ctx: MockContext): Pick<ApiClient, "ingest"> {
  return {
    async ingest() {
      await ctx.wait();
      throw new ApiError("not_found", "偽の API はまだこの口を持たない", 404);
    },
  };
}
