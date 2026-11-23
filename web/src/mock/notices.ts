// 起きたことの知らせ（GET …/notices）の偽の応答。口は置いたが、中身（知らせの偽のデータ）は後続の課題が埋める。それまでは知らせが 0 件。

import type { ApiClient } from "../api/client.ts";
import type { MockContext } from "./context.ts";

export function noticesMock(ctx: MockContext): Pick<ApiClient, "notices"> {
  return {
    async notices(name) {
      await ctx.wait();
      ctx.wsOf(name);
      return { notices: [] };
    },
  };
}
