// 2026-10-17 オーナー: 「待っている理由（直前の先行）」を、課題を押したときに開く詳細のモーダルに足す。
// モーダルは本番の部品なので、関係グラフの画面からだけ差し込み口（supplement）で渡し、ボードの表示は変えない。
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../../api/client.ts";
import type { ApiClient } from "../../api/client.ts";
import { Board } from "../../board/Board.tsx";
import type { LiveState, WorkspaceData } from "../../live/useLive.ts";
import { createMockApi } from "../../mock/api.ts";
import { MockStore } from "../../mock/store.ts";
import { GraphScreen, resetGraphMemory } from "./GraphScreen.tsx";
import { WAITING_TITLE, waitingOn } from "./WaitingReasons.tsx";

let api: ApiClient;
async function liveOf(): Promise<LiveState> {
  const { workspaces } = await api.workspaces();
  const data: Record<string, WorkspaceData> = {};
  for (const w of workspaces.filter((w) => w.state === "ok")) {
    data[w.name] = {
      status: await api.status(w.name),
      challenges: await api.challenges(w.name),
    };
  }
  return {
    connected: true,
    lastUpdated: new Date(0),
    workspaces,
    data,
    needsHuman: await api.needsHuman(),
    loadError: null,
    fleetError: null,
  };
}

beforeEach(() => {
  resetGraphMemory();
  api = createMockApi(new MockStore(), { latencyMs: 0 });
  setApi(api);
});
afterEach(() => resetApi());

const node = (id: string) =>
  within(screen.getByRole("group", { name: /の課題の関係グラフ$/ })).getByRole(
    "button",
    { name: new RegExp(`^${id} (?!を)`) },
  );
const openFromGraph = async (id: string) => {
  fireEvent.click(node(id));
  return screen.findByTestId("card-detail-content");
};
// jsdom では <dialog> が開いた状態にならず、中身を role で引けないので、要素を直接引く
const sectionOf = (modal: HTMLElement) =>
  modal.querySelector<HTMLElement>(`section[aria-label="${WAITING_TITLE}"]`);
const items = (modal: HTMLElement) =>
  [...(sectionOf(modal)?.querySelectorAll("li") ?? [])].map(
    (li) => li.textContent,
  );

it("並べ方: 課題が直接待っている依存だけを、先行の ID の順に並べる", () => {
  const rel = (from: string, to: string) =>
    ({ from, kind: "depends_on", to }) as const;
  expect(
    waitingOn("C-18", [
      rel("C-8", "C-18"),
      rel("C-6", "C-18"),
      rel("C-12", "C-6"),
      { from: "C-14", kind: "parent_of", to: "C-18" },
    ]).map((r) => r.from),
  ).toEqual(["C-6", "C-8"]);
});

it("グラフから開いた詳細: 待っている先行課題と、依存の種類・理由を出す", async () => {
  render(<GraphScreen live={await liveOf()} />);
  await screen.findByRole("group", { name: "alpha の課題の関係グラフ" });
  fireEvent.click(screen.getByRole("button", { name: "親をすべて開く" }));
  let modal = await openFromGraph("C-18");
  expect(sectionOf(modal)?.querySelector("h3")?.textContent).toBe(
    WAITING_TITLE,
  );
  expect(items(modal)).toEqual([
    "C-6 古いバッチの撤去（実施中）終わらないと始められないバッチが止まらないと旧テーブルへの書き込みが続く",
    "C-8 移行スクリプトの失敗調査終わらないと始められない移行が全件済んだと確かめてから消す",
  ]);
  // 「課題」の節の後ろに置く
  const section = sectionOf(modal);
  if (!section) throw new Error("節が無い");
  expect(
    within(modal).getByTestId("ledger-join").compareDocumentPosition(section) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  fireEvent.click(within(modal).getByText("閉じる"));
  // 衝突の種類の依存と、完了した先行
  modal = await openFromGraph("C-4");
  expect(items(modal)).toEqual([
    "C-9 画像変換のコスト超過終わらないと始められないC-9 で足す画像の権限の API を使う",
    "C-10 <img src=x onerror=alert(1)> 外部 Issue のタイトルをそのまま取り込んだ同時に走らせると衝突する同じ画面の部品（AdminToolbar）を触る",
  ]);
  expect(
    sectionOf(modal)?.querySelector('li[data-from="C-10"]'),
  ).toHaveAttribute("data-basis", "conflict");
  fireEvent.click(within(modal).getByText("閉じる"));
  modal = await openFromGraph("C-6");
  expect(items(modal)[0]).toContain("C-12 古い設定の削除（完了）");
  fireEvent.click(within(modal).getByText("閉じる"));
  // 待っている先行が無い課題には節を出さない
  modal = await openFromGraph("C-12");
  expect(sectionOf(modal)).toBeNull();
});

it("ボードから開いた詳細: 差し込みを渡さないので「待っている理由」を出さない（ボードの表示は変わらない）", async () => {
  render(<Board live={await liveOf()} />);
  const card = await screen.findByTestId("card-C-18");
  fireEvent.click(within(card).getByRole("button"));
  const modal = await screen.findByTestId("card-detail-content");
  expect(sectionOf(modal)).toBeNull();
  expect(modal.querySelector(".graph-waiting")).toBeNull();
  expect(modal.textContent).not.toContain("待っている理由");
});
