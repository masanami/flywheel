import { type Locator, type Page, expect, test } from "@playwright/test";
import { BASE_URL_ENV } from "./global-setup";

// 関係グラフを、本物の `flywheel serve` と種のストアで確かめる（flywheel#463。docs/features/ui-relation-graph.md
// §本物の serve の E2E）。種は internal/core/relation_graph_seed_test.go の TestRelationGraphSeed が作る:
//   C-1 人間待ち（未回答の問い）→ C-2 → C-3（依存で 2 段）。C-12（完了）→ C-1、C-14（取り下げ）→ C-3
//   C-4 着手中 ⊃ C-5 ⊃ C-6（親子で 2 段）
//   C-7 P0 の計画待ち・C-8 P0 の分類待ち（線なし）
//   C-9（線なし）・C-10 → C-11（起点を含まないまとまり）＝ほかの課題
//   C-13 完了・C-17 取り下げ（線なし。既定で隠れる）。C-18 完了 → C-12（完了）は既定で隠れ、線も描かれない
//   C-15 完了 ⊃ C-16 人間待ち（不可逆操作の承認）
//   C-19 完了 → C-11（ほかの課題の大枠の中に灰色で残る）

test.use({
  baseURL: process.env[BASE_URL_ENV],
  viewport: { width: 1280, height: 900 },
});

const openGraph = async (page: Page) => {
  await page.goto("/");
  const graph = page.getByRole("group", { name: "seed の課題の関係グラフ" });
  await expect(graph).toBeVisible();
  return graph;
};
// ノードそのもの（「C-4 を開く」などの開閉ボタンを除く）
const node = (graph: Locator, id: string) =>
  graph.getByRole("button", { name: new RegExp(`^${id} (?!を)`) });
const othersClosed = (graph: Locator) =>
  graph.getByRole("button", { name: /^ほかの課題を開く（\d+ 件）/ });
const categoryOf = (n: Locator) =>
  n.evaluate((el) =>
    el.closest("[data-category]")?.getAttribute("data-category"),
  );
// 課題の箱が、外側の枠の中に収まっているか
const inside = async (outer: Locator, inner: Locator) => {
  const o = await outer.boundingBox();
  const n = await inner.boundingBox();
  return (
    !!o &&
    !!n &&
    n.x >= o.x &&
    n.y >= o.y &&
    n.x + n.width <= o.x + o.width &&
    n.y + n.height <= o.y + o.height
  );
};
const groupBox = (graph: Locator, id: string) =>
  graph
    .locator(
      `.graph-group:has(> .graph-group-head[aria-label^="${id} "]) > .graph-group-box`,
    )
    .first();

test("① 取り下げの課題は取り下げの区分で、札「取り下げ」つきで出る", async ({
  page,
}) => {
  const graph = await openGraph(page);
  const withdrawn = node(graph, "C-14");
  await expect(withdrawn).toBeVisible();
  expect(await categoryOf(withdrawn)).toBe("withdrawn");
  await expect(withdrawn.locator(".graph-node-withdrawn-tag")).toHaveText(
    "取り下げ（わざと壊した期待値）",
  );
  // 題名（「C-3 の別の前提」）に無い区分の語が、読み上げの名前に入る
  await expect(withdrawn).toHaveAccessibleName(
    /^C-14 C-3 の別の前提（取り下げ/,
  );
});

test("② 終わった課題は既定で隠れ、前提・親のものだけ灰色で残り、「終わった課題もすべて表示」で全部出る", async ({
  page,
}) => {
  const graph = await openGraph(page);
  // 前提（C-12 → C-1）と親（C-15 ⊃ C-16）の完了は灰色（完了の区分）で残る
  await expect(node(graph, "C-12")).toBeVisible();
  expect(await categoryOf(node(graph, "C-12"))).toBe("done");
  const parentHead = graph.locator('.graph-group-head[aria-label^="C-15 "]');
  await expect(parentHead).toBeVisible();
  expect(await categoryOf(parentHead)).toBe("done");
  // どこにもつながらない完了 C-13・取り下げ C-17 と、終わった課題の前提でしかない C-18 は隠れる
  for (const id of ["C-13", "C-17", "C-18"])
    await expect(node(graph, id)).toHaveCount(0);

  const all = page.getByRole("checkbox", { name: "終わった課題もすべて表示" });
  await expect(all).not.toBeChecked();
  await page.locator("label.graph-switch").click();
  await expect(all).toBeChecked();
  for (const id of ["C-12", "C-13", "C-14", "C-17", "C-18"])
    await expect(node(graph, id)).toBeVisible();
  await page.locator("label.graph-switch").click();
  await expect(all).not.toBeChecked();
  for (const id of ["C-13", "C-17", "C-18"])
    await expect(node(graph, id)).toHaveCount(0);
});

test("③ 「ほかの課題」の大枠は、注目とどの線でもつながらないまとまり（C-9・C-10・C-11 と、その前提の完了 C-19）だけで、既定で畳み、押しても詳細は開かない", async ({
  page,
}) => {
  const graph = await openGraph(page);
  const closed = othersClosed(graph);
  await expect(closed).toBeVisible();
  for (const id of ["C-9", "C-10", "C-11", "C-19"])
    await expect(node(graph, id)).toHaveCount(0);
  // 注目のまとまりは、大枠が畳まれていても見えている（親の中の C-5・C-6 は親を開いて見る）
  for (const id of ["C-1", "C-2", "C-3", "C-7", "C-8", "C-12", "C-14", "C-16"])
    await expect(node(graph, id), id).toBeVisible();
  await closed.click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const frame = graph.locator(".graph-others-frame > .graph-group-box");
  // 残した終わった課題（C-19）は、属するまとまりに従って大枠の中に灰色で置かれる
  expect(await categoryOf(node(graph, "C-19"))).toBe("done");
  for (const id of ["C-9", "C-10", "C-11", "C-19"]) {
    await expect(node(graph, id)).toBeVisible();
    expect(
      await inside(frame, node(graph, id).locator(".graph-node-box")),
      id,
    ).toBe(true);
  }
  // 注目の課題は大枠の中に無い（見えていることを先に確かめる。見えないと位置が取れず false になるため）
  for (const id of [
    "C-1",
    "C-2",
    "C-3",
    "C-7",
    "C-8",
    "C-12",
    "C-14",
    "C-16",
  ]) {
    await expect(node(graph, id), id).toBeVisible();
    expect(
      await inside(frame, node(graph, id).locator(".graph-node-box")),
      id,
    ).toBe(false);
  }
  await graph.getByRole("button", { name: /^ほかの課題を畳む/ }).click();
  await expect(node(graph, "C-9")).toHaveCount(0);
});

test("④ 依存の線と、親子の入れ子が出る", async ({ page }) => {
  const graph = await openGraph(page);
  for (const [from, to] of [
    ["C-12", "C-1"],
    ["C-1", "C-2"],
    ["C-2", "C-3"],
    ["C-14", "C-3"],
  ])
    await expect(
      graph.locator(`.graph-edge-hit[aria-label^="依存 ${from} → ${to}（"]`),
      `${from} → ${to}`,
    ).toHaveCount(1);
  // 隠れた課題（C-18）につながる線は描かない。種の線は 7 本で、C-10 → C-11・C-19 → C-11 は畳んだ大枠の中
  const hiddenEdge = graph.locator(
    '.graph-edge-hit[aria-label^="依存 C-18 → C-12（"]',
  );
  await expect(hiddenEdge).toHaveCount(0);
  await expect(graph.locator(".graph-edge-hit")).toHaveCount(4);
  await othersClosed(graph).click();
  for (const [from, to] of [
    ["C-10", "C-11"],
    ["C-19", "C-11"],
  ])
    await expect(
      graph.locator(`.graph-edge-hit[aria-label^="依存 ${from} → ${to}（"]`),
      `${from} → ${to}`,
    ).toHaveCount(1);
  // 全部を出すと、C-18 とその線が出る
  await page.locator("label.graph-switch").click();
  await expect(hiddenEdge).toHaveCount(1);
  await page.locator("label.graph-switch").click();

  // 親子は入れ子: C-15 の大枠の中に C-16
  expect(
    await inside(
      groupBox(graph, "C-15"),
      node(graph, "C-16").locator(".graph-node-box"),
    ),
  ).toBe(true);
});

test("⑤ 人間の操作待ちの課題に、区分と何をするかの札が出る", async ({
  page,
}) => {
  const graph = await openGraph(page);
  const c1 = node(graph, "C-1");
  expect(await categoryOf(c1)).toBe("human");
  await expect(c1).toHaveAccessibleName(/人間の操作待ち/);
  await expect(c1.locator(".graph-human-chip-glyph")).toHaveText("?");
  await expect(c1.locator(".graph-human-chip-label")).toHaveText(
    "未回答の問い",
  );
  const c16 = node(graph, "C-16");
  expect(await categoryOf(c16)).toBe("human");
  await expect(c16.locator(".graph-human-chip-glyph")).toHaveText("!");
  await expect(c16.locator(".graph-human-chip-label")).toHaveText(
    "不可逆操作の承認",
  );
  // 人間の操作待ちでないノードには札が無い
  await expect(node(graph, "C-2").locator(".graph-human-chip")).toHaveCount(0);
});

test("⑥ 優先度 P0 の分類待ち・計画待ちは、線が無くても大枠に入らず前に出る", async ({
  page,
}) => {
  const graph = await openGraph(page);
  // 大枠は畳んだまま（C-7・C-8 は中身ではない）でも見えている
  await expect(othersClosed(graph)).toBeVisible();
  const field = (dialog: Locator, name: string) =>
    dialog.locator(`.card-detail-fields dt:text-is("${name}") + dd`).first();
  for (const [id, status] of [
    ["C-7", "分類済"],
    ["C-8", "未分類"],
  ] as const) {
    await expect(node(graph, id)).toBeVisible();
    await node(graph, id).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await expect(field(dialog, "ステータス")).toHaveText(status);
    await expect(field(dialog, "優先度")).toHaveText("P0");
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  }
});

test("⑦ 推移的な注目: 起点から 2 段以上先の課題（依存の C-3・親子の C-6）も大枠に入らず前に出る", async ({
  page,
}) => {
  const graph = await openGraph(page);
  // 大枠は畳んだままでも、依存で 2 段先の C-3 は見えている
  await expect(othersClosed(graph)).toBeVisible();
  await expect(node(graph, "C-3")).toBeVisible();
  // 着手中の C-4 の子 C-5・孫 C-6 は、親の入れ子のまま前に出る
  // 既定で開いているか（実施中・人間待ちの子を持つ親だけが開く）は問わず、畳んでいれば開く
  for (const id of ["C-4", "C-5"]) {
    const open = graph.getByRole("button", {
      name: new RegExp(`^${id} を開く`),
    });
    if ((await open.count()) > 0) await open.click();
    await expect(
      graph.getByRole("button", { name: `${id} を畳む` }),
    ).toBeVisible();
  }
  await expect(node(graph, "C-6")).toBeVisible();
  expect(
    await inside(
      groupBox(graph, "C-5"),
      node(graph, "C-6").locator(".graph-node-box"),
    ),
  ).toBe(true);
  expect(
    await inside(
      groupBox(graph, "C-4"),
      graph.locator('.graph-group-head[aria-label^="C-5 "]'),
    ),
  ).toBe(true);
  // 大枠を開いても、C-3・C-4・C-5・C-6 は大枠の中に無い
  await othersClosed(graph).click();
  const frame = graph.locator(".graph-others-frame > .graph-group-box");
  await expect(frame).toBeVisible();
  for (const target of [
    node(graph, "C-3").locator(".graph-node-box"),
    graph.locator('.graph-group-head[aria-label^="C-4 "]'),
    graph.locator('.graph-group-head[aria-label^="C-5 "]'),
    node(graph, "C-6").locator(".graph-node-box"),
  ]) {
    await expect(target).toBeVisible();
    expect(await inside(frame, target)).toBe(false);
  }
});
