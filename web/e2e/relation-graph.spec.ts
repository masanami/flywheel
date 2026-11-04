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

  test("終わった課題は既定で隠れ、前提になっている取り下げは札つきで残り、切り替えで全部出る", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    await expect(graph).toBeVisible();
    // C-21（取り下げ）は動いている C-18 の前提なので出て、「取り下げ」の札で区別される
    const withdrawn = graph.getByRole("button", { name: /^C-21 / });
    await expect(withdrawn).toBeVisible();
    await expect(withdrawn.locator(".graph-node-withdrawn-tag")).toHaveText(
      "取り下げ",
    );
    // 何ともつながらない完了の C-22 は隠れる
    await expect(graph.getByRole("button", { name: /^C-22 / })).toHaveCount(0);
    await expect(
      page.getByRole("checkbox", { name: "完了を隠す" }),
    ).toHaveCount(0);
    const all = page.getByRole("checkbox", {
      name: "終わった課題もすべて表示",
    });
    await expect(all).not.toBeChecked();
    await page.locator("label.graph-switch").click();
    await expect(all).toBeChecked();
    await expect(graph.getByRole("button", { name: /^C-22 / })).toBeVisible();
    await page.locator("label.graph-switch").click();
    await expect(all).not.toBeChecked();
    await expect(graph.getByRole("button", { name: /^C-22 / })).toHaveCount(0);
  });

  test("ほかの課題: 親も子も依存も持たない分類待ち・計画待ちの課題だけを大枠にまとめ、既定で畳み、押しても詳細は開かず、開閉できる", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    // 人間の操作待ち（C-3・C-7）と実施中（C-20）が線無しでいても、大枠は畳んだまま。中身（C-23・C-24）は見えない
    const closed = graph.getByRole("button", {
      name: /^ほかの課題を開く（2 件）/,
    });
    await expect(closed).toBeVisible();
    await expect(graph.getByRole("button", { name: /^C-23 / })).toHaveCount(0);
    await expect(graph.getByRole("button", { name: /^C-24 / })).toHaveCount(0);
    // 線の無い人間の操作待ち・着手中は、大枠に入らずそのまま並ぶ
    for (const id of ["C-3", "C-7", "C-20"])
      await expect(
        graph.getByRole("button", { name: new RegExp(`^${id} `) }),
      ).toBeVisible();
    await closed.click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(graph.getByRole("button", { name: /^C-23 / })).toBeVisible();
    await expect(graph.getByRole("button", { name: /^C-24 / })).toBeVisible();
    await graph.getByRole("button", { name: /^ほかの課題を畳む/ }).click();
    await expect(graph.getByRole("button", { name: /^C-23 / })).toHaveCount(0);
  });

  test("人間の操作待ちのノードに「何をするか」の札が帯の語で出る（承認と不可逆操作は並び、実装の語は出ない）", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    const tag = (id: string) =>
      graph
        .getByRole("button", { name: new RegExp(`^${id} `) })
        .locator(".graph-node-human-tag");
    await expect(tag("C-3")).toHaveText("承認待ち");
    await expect(tag("C-7")).toHaveText("承認待ち・不可逆操作の承認");
    // 開いた大枠 C-14 の中の C-8（問いに答える）
    await expect(tag("C-8")).toHaveText("未回答の問い");
    // 人間の操作待ちでないノードには札が無い
    await expect(tag("C-20")).toHaveCount(0);
    await expect(tag("C-5")).toHaveCount(0);
    const html = await page.content();
    expect(html).not.toMatch(
      /approve_plan|approve_completion|answer_question|approve_operation|next_human_actions/,
    );
  });

  test("詳細の長い文章にだけ「全文を開く」が出る（短い・3 行に収まる文章には出ない）", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    // C-3: 短い論点・複数行でも 3 行に収まる文章だけ
    await graph.getByRole("button", { name: /^C-3 / }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await expect(
      dialog
        .getByTestId("stages-detail")
        .getByText("再送の重複をどこで止めますか"),
    ).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: "全文を開く" }),
    ).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);

    // C-10: 何行にも伸びる長い文章
    await graph.getByRole("button", { name: /^C-10 / }).click();
    const long = page.getByRole("dialog");
    const open = long.getByRole("button", { name: "全文を開く" }).first();
    await expect(open).toBeVisible();
    // 文字色はリンクの色ではなく、控えめなグレー（--board-muted-fg）。ライトでもダークでも
    const sameAsMuted = () =>
      open.evaluate((el) => {
        const probe = document.createElement("span");
        probe.style.color = "var(--board-muted-fg)";
        document.body.append(probe);
        const muted = getComputedStyle(probe).color;
        probe.remove();
        return getComputedStyle(el).color === muted;
      });
    expect(await sameAsMuted()).toBe(true);
    await page.emulateMedia({ colorScheme: "dark" });
    expect(await sameAsMuted()).toBe(true);
    await page.emulateMedia({ colorScheme: "light" });
    await open.click();
    await expect(
      long.getByRole("button", { name: "折りたたむ" }).first(),
    ).toBeVisible();
    await long.getByRole("button", { name: "折りたたむ" }).first().click();
    await expect(
      long.getByRole("button", { name: "全文を開く" }).first(),
    ).toBeVisible();
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

  test("終わった課題は既定で隠れ、ほかの課題は分類待ち・計画待ちだけで既定で畳んでいる。人間の操作待ちの行には札が出る", async ({
    page,
  }) => {
    await page.goto("/");
    const list = page.getByRole("list", { name: "課題の一覧（親ごと）" });
    await expect(list).toBeVisible();
    await expect(list.getByRole("button", { name: /^C-22 / })).toHaveCount(0);
    await expect(list.getByRole("button", { name: /^C-21 / })).toBeVisible();
    // 大枠は畳んでいる（人間の操作待ち C-3・C-7 が線無しでいても開かない）
    const others = list.getByRole("button", {
      name: /^ほかの課題を開く（2 件）/,
    });
    await expect(others).toBeVisible();
    await expect(list.getByRole("button", { name: /^C-23 / })).toHaveCount(0);
    // 線の無い実施中・人間の操作待ちは大枠の外に並ぶ
    await expect(list.getByRole("button", { name: /^C-20 / })).toBeVisible();
    const c7 = list.locator(
      'li.graph-list-item:has(> .graph-list-row > button.graph-list-open[aria-label^="C-7 "])',
    );
    await expect(c7.locator(".graph-list-human")).toHaveText(
      "承認待ち・不可逆操作の承認",
    );
    await others.click();
    await expect(list.getByRole("button", { name: /^C-23 / })).toBeVisible();
    await page.locator("label.graph-switch").click();
    await expect(list.getByRole("button", { name: /^C-22 / })).toBeVisible();
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
