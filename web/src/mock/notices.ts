// 起きたことの知らせ（GET …/notices）の偽の応答。alpha だけが持つ。4 種を 1 件ずつ、新しい順に返す。
// 題名は偽の課題・タスクから引く（本物は、記録から作った知らせへ、サーバが題名を引いて写す）。

import type { ApiClient } from "../api/client.ts";
import type { Notice } from "../api/types.gen.ts";
import type { MockContext } from "./context.ts";

const at = (minutesAgo: number) =>
  new Date(Date.now() - minutesAgo * 60_000).toISOString();

const base: Notice = {
  added_count: null,
  at: "",
  challenge_id: null,
  challenge_title: null,
  cycle_id: null,
  end_reason: null,
  kind: "",
  origin: null,
  task_id: null,
  task_title: null,
};

export function noticesMock(ctx: MockContext): Pick<ApiClient, "notices"> {
  return {
    async notices(name) {
      await ctx.wait();
      const ws = ctx.wsOf(name);
      if (name !== "alpha") return { notices: [] };
      const out: Notice[] = [];
      const withTask = ws.items.find((m) =>
        m.holds.some((h) => h.task_id !== null),
      );
      const hold = withTask?.holds.find((h) => h.task_id !== null);
      if (withTask && hold && hold.task_id !== null) {
        out.push({
          ...base,
          at: at(5),
          kind: "supervisor_escalated",
          challenge_id: withTask.c.id,
          challenge_title: withTask.c.title,
          task_id: hold.task_id,
          task_title: "上げられたタスク",
        });
      }
      out.push({
        ...base,
        at: at(30),
        kind: "cycle_ended",
        cycle_id: "Y-3",
        end_reason: "budget",
      });
      const second = ws.items[1];
      if (second) {
        out.push({
          ...base,
          at: at(90),
          kind: "released",
          challenge_id: second.c.id,
          challenge_title: second.c.title,
        });
      }
      const first = ws.items[0];
      if (first) {
        out.push({
          ...base,
          at: at(150),
          kind: "task_added",
          challenge_id: first.c.id,
          challenge_title: first.c.title,
          added_count: 2,
          origin: "followup",
        });
      }
      return { notices: out };
    },
  };
}
