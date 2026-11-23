// フォローアップを済みにする・捨てる（POST …/followups/done・drop）の偽の応答。
// 一覧の偽のデータ（ALPHA_FOLLOWUPS）を、ブラウザ内だけで書き換える。選んだものの 1 つでも未でなければ、どれも変えない（本物の口と同じく 400 validation_failed）。
// 一覧の偽のデータは followups.ts。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { Followup } from "../api/types.gen.ts";
import type { MockContext } from "./context.ts";
import { ALPHA_FOLLOWUPS } from "./followups.ts";

export function followupActionsMock(
  ctx: MockContext,
): Pick<ApiClient, "followupsDone" | "followupsDrop"> {
  const change = async (
    name: string,
    ids: string[],
    note: string,
    status: "done" | "dropped",
  ) => {
    await ctx.wait();
    ctx.wsOf(name);
    // 本物の口（followups_done.go・followups_drop.go）と同じ順: ids（空・同じ ID の 2 度の指定）、文、存在、未であること。
    if (ids.length === 0 || new Set(ids).size !== ids.length)
      throw new ApiError(
        "validation_failed",
        "ids は 1 件以上で、同じ ID を 2 度指定できない",
        400,
      );
    if (note.trim() === "")
      throw new ApiError("validation_failed", "文が要る", 400);
    const all: Followup[] = name === "alpha" ? ALPHA_FOLLOWUPS : [];
    const picked = ids.map((id) => all.find((f) => f.id === id));
    if (picked.some((f) => f === undefined))
      throw new ApiError("not_found", "そのフォローアップはありません", 404);
    if (picked.some((f) => f?.status !== "open"))
      throw new ApiError(
        "validation_failed",
        "未ではないものが含まれています",
        400,
      );
    for (const f of picked) {
      if (!f) continue;
      f.status = status;
      if (status === "done") f.note = note;
      else f.reason = note;
    }
    return { followups: all };
  };
  return {
    followupsDone: (name, req) => change(name, req.ids, req.note, "done"),
    followupsDrop: (name, req) => change(name, req.ids, req.reason, "dropped"),
  };
}
