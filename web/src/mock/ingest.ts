// 画面のボタンでの取り込み（POST …/ingest）の偽の応答。本物の口の断り方（空の指定・取り込めるものでない指定・重複は 400）を写す。
// 成立したら、指定ごとに課題を 1 つ足し（未分類）、取り込めるものの一覧から外す。サイクルは、走っていなければ始めた形で返す。

import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { MockContext } from "./context.ts";
import { INGESTED_KEYS, ingestableOf, mk } from "./store.ts";

export function ingestMock(ctx: MockContext): Pick<ApiClient, "ingest"> {
  return {
    async ingest(name, req) {
      await ctx.wait();
      const ws = ctx.wsOf(name);
      if (req.items.length === 0) {
        throw new ApiError("validation_failed", "指定が要ります", 400);
      }
      if (new Set(req.items).size !== req.items.length) {
        throw new ApiError(
          "validation_failed",
          "同じ指定が重なっています",
          400,
        );
      }
      const available = ingestableOf(name).items;
      const picked = req.items.map((k) =>
        available.find((it) => it.external_key === k),
      );
      if (picked.some((it) => it === undefined)) {
        throw new ApiError(
          "validation_failed",
          "取り込めるものでない指定が含まれています",
          400,
        );
      }
      let n = ws.items.length;
      const ingested = [];
      for (const it of picked) {
        if (it === undefined) continue;
        n += 1;
        const id = `C-ing-${n}`;
        ws.items.push(mk(id, it.title, "unclassified"));
        INGESTED_KEYS.add(it.external_key);
        ingested.push({
          challenge_id: id,
          children: it.children.length,
          external_key: it.external_key,
        });
      }
      const running = ws.cycle.state === "running";
      ctx.store.emit(name);
      return {
        cycle: {
          reason: running ? "already_running" : "started",
          started: !running,
        },
        ingested,
      };
    },
  };
}
