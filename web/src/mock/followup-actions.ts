// フォローアップを済みにする・捨てる（POST …/followups/done・drop）の偽の応答。口は置いたが、中身は後続の課題が埋める。
// 一覧の偽のデータは followups.ts。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { MockContext } from "./context.ts";

export function followupActionsMock(
  ctx: MockContext,
): Pick<ApiClient, "followupsDone" | "followupsDrop"> {
  const notYet = async () => {
    await ctx.wait();
    throw new ApiError("not_found", "偽の API はまだこの口を持たない", 404);
  };
  return { followupsDone: notYet, followupsDrop: notYet };
}
