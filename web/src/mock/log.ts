import type { Activity } from "../api/types.gen.ts";

// 作業ログの偽の記録（課題の ID つきの `GET …/log`）。C-28 は、経緯の各種の行（対話・画面・ターミナル・実行の経路、
// 知らない action、長い題名・長い発言の要旨）を持つ。ほかの課題は、課題を作った 1 件だけ。
const act = (
  p: Partial<Activity> & Pick<Activity, "at" | "action">,
): Activity => ({
  actor: "owner",
  after: null,
  before: null,
  channel: "cli",
  entity: "challenge",
  entity_id: "C-28",
  verification: "none",
  ...p,
});

const T = (min: number) =>
  new Date(Date.parse("2026-10-12T09:00:00Z") - min * 60_000).toISOString();

export function alphaLogExtras(id: string): Activity[] {
  if (id !== "C-28") return [];
  return [
    act({
      at: T(300),
      action: "plan",
      channel: "invoker",
      actor: "flywheel",
    }),
    act({
      at: T(280),
      action: "approve",
      channel: "ui",
      verification: "ui_session",
    }),
    act({
      at: T(200),
      action: "hold",
      channel: "invoker",
      actor: "flywheel",
    }),
    act({
      at: T(190),
      action: "answer",
      channel: "cli",
      verification: "relayed",
      after: {
        relay_quote:
          "入力の名前は英語にする。長い URL https://example.com/billing/issues/28/with/a/very/long/path/that/should/wrap も添える",
      },
    }),
    act({
      at: T(120),
      action: "submit",
      channel: "invoker",
      actor: "flywheel",
    }),
    act({
      at: T(100),
      action: "verify_met",
      channel: "invoker",
      actor: "flywheel",
    }),
    act({
      at: T(90),
      action: "op_approve",
      after: { operation_id: "O-1" },
      channel: "cli",
      verification: "tty_confirm",
    }),
    act({
      at: T(60),
      action: "task_status",
      entity: "task",
      entity_id: "T-40",
      channel: "invoker",
      actor: "flywheel",
    }),
    act({
      at: T(50),
      action: "unknown_new_action",
      channel: "invoker",
      actor: "flywheel",
    }),
  ];
}
