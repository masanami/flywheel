import { type Locator, type Page, expect, test } from "@playwright/test";

// flywheel#464: 人間の操作待ちのチップが 3 個以上付いたときに、3 つの置き場（広い幅のノード・開いた親の大枠の見出し・
// 幅 480px 以下の一覧の行）で、チップが題名とも「▶ 実施中」とも重ならず、横スクロールも出ないことを確かめる。
// 偽のデータ（mock モード）: C-28 は実施中でチップ 4 個（トリアージ・予算・不可逆操作・食い違い）の葉、
// C-29 は実施中でチップ 5 個（承認待ち・トリアージ・予算・不可逆操作・食い違い）の親で、実施中の子 C-30 を持つので既定で開く。

type Rect = { x: number; y: number; width: number; height: number };

const C28_LABELS = [
  "不可逆操作の承認",
  "食い違い",
  "予算の使い切り",
  "トリアージ",
];
const C29_LABELS = [
  "承認待ち",
  "不可逆操作の承認",
  "食い違い",
  "予算の使い切り",
  "トリアージ",
];

// 2 つの矩形が交わるか（辺が接するだけなら交わらない）
const intersects = (a: Rect, b: Rect) =>
  a.x < b.x + b.width &&
  b.x < a.x + a.width &&
  a.y < b.y + b.height &&
  b.y < a.y + a.height;

// a が b の中に収まるか（端の 0.5px の丸めは許す）
const inside = (a: Rect, b: Rect) =>
  a.x >= b.x - 0.5 &&
  a.y >= b.y - 0.5 &&
  a.x + a.width <= b.x + b.width + 0.5 &&
  a.y + a.height <= b.y + b.height + 0.5;

const box = async (l: Locator, what: string): Promise<Rect> => {
  const r = await l.boundingBox();
  if (!r) throw new Error(`${what} の位置が取れない`);
  return r;
};

const boxes = async (l: Locator): Promise<Rect[]> => {
  const out: Rect[] = [];
  for (const one of await l.all()) {
    const r = await one.boundingBox();
    if (r) out.push(r);
  }
  return out;
};

// チップのどれかが other と交わっていないか。交わるチップの番号を返す
const overlapping = (chips: Rect[], other: Rect) =>
  chips.flatMap((c, i) => (intersects(c, other) ? [i] : []));

// 人間の操作待ちの意味（何をするか）が失われていないか: 画面に出ているチップの語（切り詰めていないもの）と、
// まとめたチップ・切り詰めたチップの title に、待っている区分の語がすべて含まれる
const chipMeanings = (chips: Locator) =>
  chips.evaluateAll((els) =>
    els.flatMap((el) => {
      const title =
        el.getAttribute("title") ??
        el.querySelector(":scope > title")?.textContent ??
        "";
      const label =
        el.querySelector(".graph-human-chip-label")?.textContent ?? "";
      return [title, label];
    }),
  );

const expectAllMeanings = async (chips: Locator, labels: string[]) => {
  const said = (await chipMeanings(chips)).join("／");
  for (const label of labels) expect(said, label).toContain(label);
};

test.describe("広い画面（1280px）", () => {
  test.use({ viewport: { width: 1280, height: 900 } });

  test("① 広い幅のノード: チップ 4 個の実施中の課題で、チップが題名・「▶ 実施中」と重ならず、ノードの箱からはみ出さない", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    const node = graph.getByRole("button", { name: /^C-28 (?!を)/ });
    await expect(node).toBeVisible();
    const chips = node.locator(".graph-human-chip");
    expect(await chips.count()).toBeGreaterThanOrEqual(1);

    const nodeBox = await box(node.locator(".graph-node-box"), "ノードの箱");
    const title = await box(node.locator(".graph-node-title"), "題名");
    const active = await box(
      node.locator(".graph-node-active-tag"),
      "▶ 実施中",
    );
    const id = await box(node.locator(".graph-node-id"), "ID");
    const placed = await boxes(chips);
    expect(placed.length).toBe(await chips.count());

    expect(overlapping(placed, title), "題名").toEqual([]);
    expect(overlapping(placed, active), "▶ 実施中").toEqual([]);
    expect(overlapping(placed, id), "ID").toEqual([]);
    // 横にはみ出さない（SVG の中なので、ノードの箱の中に収まることで見る）
    for (const [i, c] of placed.entries())
      expect(inside(c, nodeBox), `チップ ${i}`).toBe(true);
    expect(inside(active, nodeBox), "▶ 実施中").toBe(true);
    // 題名・実施中は見えている
    await expect(node.locator(".graph-node-title")).toBeVisible();
    await expect(node.locator(".graph-node-active-tag")).toHaveText("▶ 実施中");
    // まとめたり切り詰めたりしても、何をするかの語は失わない
    await expectAllMeanings(chips, C28_LABELS);
    // ページに横スクロールが出ない
    const [sw, cw] = await page.evaluate(() => [
      document.documentElement.scrollWidth,
      document.documentElement.clientWidth,
    ]);
    expect(sw).toBeLessThanOrEqual(cw);
  });

  test("② 開いた親の大枠の見出し: チップ 5 個の実施中の親で、チップが題名・「▶ 実施中」・開閉ボタンと重ならず、見出しからはみ出さない", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    // 実施中の子 C-30 を持つので既定で開いている
    await expect(
      graph.getByRole("button", { name: "C-29 を畳む" }),
    ).toBeVisible();
    const head = graph.locator('.graph-group-head[aria-label^="C-29 "]');
    const chips = head.locator(".graph-human-chip");
    expect(await chips.count()).toBeGreaterThanOrEqual(1);

    const headBox = await box(head.locator(".graph-group-head-box"), "見出し");
    const title = await box(head.locator(".graph-group-title-text"), "題名");
    const active = await box(
      head.locator(".graph-group-active-tag"),
      "▶ 実施中",
    );
    const id = await box(head.locator(".graph-node-id"), "ID");
    const toggle = await box(
      graph.getByRole("button", { name: "C-29 を畳む" }),
      "開閉ボタン",
    );
    const placed = await boxes(chips);
    expect(placed.length).toBe(await chips.count());

    expect(overlapping(placed, title), "題名").toEqual([]);
    expect(overlapping(placed, active), "▶ 実施中").toEqual([]);
    expect(overlapping(placed, id), "ID").toEqual([]);
    expect(overlapping(placed, toggle), "開閉ボタン").toEqual([]);
    expect(intersects(active, toggle), "▶ 実施中と開閉ボタン").toBe(false);
    for (const [i, c] of placed.entries())
      expect(inside(c, headBox), `チップ ${i}`).toBe(true);
    expect(inside(active, headBox), "▶ 実施中").toBe(true);
    await expect(head.locator(".graph-group-active-tag")).toHaveText(
      /▶ 実施中/,
    );
    // 題名は詰めても 2 文字以上（と「…」）が残る
    const titleText =
      (await head.locator(".graph-group-title-text").textContent()) ?? "";
    expect(
      Array.from(titleText.trim().replace(/…$/, "")).length,
      titleText,
    ).toBeGreaterThanOrEqual(2);
    await expectAllMeanings(chips, C29_LABELS);
    const [sw, cw] = await page.evaluate(() => [
      document.documentElement.scrollWidth,
      document.documentElement.clientWidth,
    ]);
    expect(sw).toBeLessThanOrEqual(cw);
  });
});

// 一覧の行の検査。row は課題そのものの行（入れ子の li の外側ではない）
const listItem = (page: Page, id: string) =>
  page
    .getByRole("list", { name: "課題の一覧（親ごと）" })
    .locator(
      `li.graph-list-item:has(> .graph-list-row > button.graph-list-open[aria-label^="${id} "])`,
    );

for (const width of [480, 360]) {
  test.describe(`狭い画面（${width}px）`, () => {
    test.use({ viewport: { width, height: 800 } });

    for (const [id, labels] of [
      ["C-28", C28_LABELS],
      ["C-29", C29_LABELS],
    ] as const) {
      test(`③ 一覧の行: ${id}（チップ ${labels.length} 個・実施中）のチップが題名・「▶ 実施中」と重ならず、横スクロールが出ない`, async ({
        page,
      }) => {
        await page.goto("/");
        const item = listItem(page, id);
        await expect(item).toHaveCount(1);
        const row = item.locator("> .graph-list-row");
        const chips = row.locator(".graph-human-chip");
        expect(await chips.count()).toBeGreaterThanOrEqual(1);

        const title = await box(row.locator(".graph-list-open"), "題名");
        const active = await box(
          item.locator("> .graph-list-meta").getByText("▶ 実施中"),
          "▶ 実施中",
        );
        const placed = await boxes(chips);
        expect(placed.length).toBe(await chips.count());
        expect(overlapping(placed, title), "題名").toEqual([]);
        expect(overlapping(placed, active), "▶ 実施中").toEqual([]);
        // 題名は見える幅を保つ（チップに押し潰されない）
        expect(title.width).toBeGreaterThanOrEqual(80);

        // 行・チップの置き場・各チップで横にあふれない（scrollWidth ≤ clientWidth）
        const overflow = await row.evaluate((r) => {
          const els = [
            r,
            ...Array.from(
              r.querySelectorAll(
                ".graph-list-human, .graph-human-chip, .graph-human-chip-label",
              ),
            ),
          ];
          return els
            .filter((el) => el.scrollWidth > el.clientWidth)
            .map((el) => `${el.className}:${el.scrollWidth}>${el.clientWidth}`);
        });
        expect(overflow).toEqual([]);
        const [sw, cw] = await page.evaluate(() => [
          document.documentElement.scrollWidth,
          document.documentElement.clientWidth,
        ]);
        expect(sw).toBeLessThanOrEqual(cw);
        await expectAllMeanings(chips, labels);
      });
    }
  });
}
