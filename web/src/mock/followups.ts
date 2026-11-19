// フォローアップの一覧の API の偽の応答（エージェント alpha）。未 3・済み 2・捨てた 1 件。
// 課題に属するもの（C-11・C-20）と、属さないもの、上げた先の課題（C-12）を持つ。開発時の mock モードとブラウザの試験だけが使う。

import type { Followup } from "../api/types.gen.ts";

const f = (id: string, p: Partial<Followup>): Followup => ({
  challenge_id: null,
  created_at: "2026-11-10T01:00:00.000Z",
  detail: "",
  id,
  note: null,
  promoted_challenge_id: null,
  reason: null,
  source: "サイクルの振り返り",
  status: "open",
  title: id,
  updated_at: "2026-11-10T01:00:00.000Z",
  ...p,
});

export const ALPHA_FOLLOWUPS: Followup[] = [
  f("F-1", {
    challenge_id: "C-11",
    title: "反映の後に、実際の差し戻しで同じ成果物が続くかを 1 回確かめる",
    source: "完了の検証が回した条件",
    detail: "差し戻しの後も、同じ成果物のままタスクが続くことを見る。",
  }),
  f("F-2", {
    challenge_id: "C-11",
    title: "入力の名前を、ほかの入力と同じ並びにそろえる",
    source: "設計の軽い指摘",
  }),
  f("F-3", {
    title: "計画の指示文で、完了条件の数が多すぎる例が続いた。上限の目安を書く",
  }),
  f("F-4", {
    challenge_id: "C-20",
    status: "done",
    title: "古い作業場所が残っていたときの文言を見直す",
    source: "タスクの気づき",
    note: "文言を直し、実物の画面で確かめた。",
  }),
  f("F-5", {
    status: "done",
    title: "長い説明のときの切り詰め方を決める",
    note: "既存の部品で足りると確かめた。",
    promoted_challenge_id: "C-12",
  }),
  f("F-6", {
    challenge_id: "C-11",
    status: "dropped",
    title: "通信の失敗のやり直しの回数を数える",
    reason: "別の課題で扱うことにした。",
  }),
];
