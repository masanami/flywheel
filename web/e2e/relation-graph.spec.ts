import { type Page, expect, test } from "@playwright/test";

// flywheel#176 の完了条件 4・5 を、偽の API（mock モード）の上で確かめる。
// 偽のデータ: alpha（C-1〜C-20。親 C-2・C-14・C-15・C-16）が主、beta は小さく、gamma は読めない。

// 横スクロールが出ていないか。ページ全体の幅に加えて、横に送れる入れ物（overflow-x が auto・scroll）の中で
// 中身があふれていないか、見えている要素が画面の右端を越えていないかを見る（入れ物の中のあふれはページの幅に出ないため）。
const horizontalOverflow = (page: Page) =>
  page.evaluate(() => {
    const width = document.documentElement.clientWidth;
    const found: string[] = [];
    if (document.documentElement.scrollWidth > width) {
      found.push(`page:${document.documentElement.scrollWidth}`);
    }
    for (const el of Array.from(document.body.querySelectorAll("*"))) {
      const style = getComputedStyle(el);
      if (style.display === "none" || style.visibility === "hidden") continue;
      const scrollable =
        style.overflowX === "auto" || style.overflowX === "scroll";
      if (scrollable && el.scrollWidth > el.clientWidth + 1) {
        found.push(`scroll:${el.className}`);
      }
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.right > width + 1)
        found.push(`edge:${el.className}`);
    }
    return found.slice(0, 5);
  });

test.describe("広い画面（1280px）", () => {
  test.use({ viewport: { width: 1280, height: 900 } });

  test("既定はグラフで、ボードへ切り替えて戻せる", async ({ page }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    await expect(graph).toBeVisible();
    await expect(
      page.getByRole("button", { name: "グラフ", exact: true }),
    ).toHaveAttribute("aria-pressed", "true");

    await page.getByRole("button", { name: "ボード", exact: true }).click();
    await expect(page.getByRole("region", { name: "ボード" })).toBeVisible();
    await expect(graph).toHaveCount(0);

    await page.getByRole("button", { name: "グラフ", exact: true }).click();
    await expect(graph).toBeVisible();
  });

  test("親の大枠を畳んで開ける", async ({ page }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    await expect(graph).toBeVisible();
    // 既定で開いている親（実施中か人間待ちの子を持つ）C-14 を畳む
    await graph.getByRole("button", { name: "C-14 を畳む" }).click();
    const reopen = graph.getByRole("button", {
      name: /^C-14 を開く（子 \d+ 件）$/,
    });
    await expect(reopen).toBeVisible();
    await reopen.click();
    await expect(
      graph.getByRole("button", { name: "C-14 を畳む" }),
    ).toBeVisible();
  });

  test("人間の操作待ちの課題は区分で区別され、押すと詳細と待っている理由が出る", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    await expect(
      graph.getByRole("button", { name: /^C-3 .*（人間の操作待ち/ }),
    ).toBeVisible();

    const c6 = graph.getByRole("button", { name: /^C-6 / });
    await c6.click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await expect(
      dialog.getByText("待っている理由（直前の先行）"),
    ).toBeVisible();
    await expect(
      dialog.getByText("C-12", { exact: false }).first(),
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  });

  test("ボードから開いた詳細には待っている理由が出ない", async ({ page }) => {
    await page.goto("/");
    await page.getByRole("button", { name: "ボード", exact: true }).click();
    await page.getByTestId("card-C-6").first().getByRole("button").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText("待っている理由（直前の先行）")).toHaveCount(
      0,
    );
  });
});

test.describe("狭い画面（360px）", () => {
  test.use({ viewport: { width: 360, height: 780 } });

  test("グラフの代わりに親ごとの一覧が出て、先行・後続を文字で示し、横スクロールが出ない", async ({
    page,
  }) => {
    await page.goto("/");
    const list = page.getByRole("list", { name: "課題の一覧（親ごと）" });
    await expect(list).toBeVisible();
    await expect(
      page.getByRole("group", { name: "alpha の課題の関係グラフ" }),
    ).toHaveCount(0);

    // 入れ子の li の外側ではなく、C-6 そのものの行の文字を見る
    const c6 = list.locator(
      'li.graph-list-item:has(> .graph-list-row > button.graph-list-open[aria-label^="C-6 "]) > .graph-list-rel',
    );
    await expect(c6).toHaveCount(1);
    await expect(c6.getByText("先行: C-12")).toBeVisible();
    await expect(c6.getByText("後続: C-18")).toBeVisible();
    expect(await horizontalOverflow(page)).toEqual([]);
  });

  test("親をすべて開いても、詳細を開いても横スクロールが出ない", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(
      page.getByRole("list", { name: "課題の一覧（親ごと）" }),
    ).toBeVisible();
    const openAll = page.getByRole("button", { name: "親をすべて開く" });
    if (await openAll.isVisible()) await openAll.click();
    expect(await horizontalOverflow(page)).toEqual([]);

    await page.getByRole("button", { name: /^C-6 / }).first().click();
    await expect(page.getByRole("dialog")).toBeVisible();
    expect(await horizontalOverflow(page)).toEqual([]);
  });

  test("一覧の親を畳むと子が隠れる", async ({ page }) => {
    await page.goto("/");
    const list = page.getByRole("list", { name: "課題の一覧（親ごと）" });
    await list.getByRole("button", { name: "C-14 を畳む" }).click();
    await expect(
      list.getByRole("button", { name: /^C-14 を開く/ }),
    ).toBeVisible();
    await expect(list.getByRole("button", { name: /^C-12 / })).toHaveCount(0);
  });
});
