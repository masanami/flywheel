// サイクルの口の偽の応答。サイクルを止める指示（POST …/cycle/stop）と、サイクルの様子に足す値（supervisor_actions・costs）。
// 口は置いたが、中身は後続の課題が埋める。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { CycleScreen } from "../api/types.gen.ts";
import type { MockContext } from "./context.ts";

/** サイクルの様子（CycleStatus の欄）に足す、対象のサイクルのスーパーバイザーの手と費用の内訳。 */
export function cycleScreenExtras(
  _ctx: MockContext,
  _name: string,
): Pick<CycleScreen, "supervisor_actions" | "costs"> {
  return { supervisor_actions: [], costs: [] };
}

export function cycleMock(ctx: MockContext): Pick<ApiClient, "cycleStop"> {
  return {
    async cycleStop() {
      await ctx.wait();
      throw new ApiError("not_found", "偽の API はまだこの口を持たない", 404);
    },
  };
}
