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

// 色（rgb(...) か color(srgb r g b)）を 0〜1 の RGB にする
const parseColor = (c: string): [number, number, number] => {
  const nums = (c.match(/-?[\d.]+/g) ?? []).map(Number);
  const [r = 0, g = 0, b = 0] = nums;
  return c.startsWith("color(") ? [r, g, b] : [r / 255, g / 255, b / 255];
};
const luminance = ([r, g, b]: [number, number, number]) => {
  const f = (v: number) =>
    v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
};
const contrast = (a: string, b: string) => {
  const [hi, lo] = [luminance(parseColor(a)), luminance(parseColor(b))].sort(
    (x, y) => y - x,
  );
  return ((hi ?? 0) + 0.05) / ((lo ?? 0) + 0.05);
};

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
    await expect(dialog.getByText("待っている理由")).toBeVisible();
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

  test("ほかの課題: 注目の課題とどの線でもつながらない課題のまとまりだけを大枠にまとめ、既定で畳み、押しても詳細は開かず、開閉できる。注目は推移的に前に出る", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    const nodeBtn = (id: string) =>
      graph.getByRole("button", { name: new RegExp(`^${id} (?!を)`) });
    // 大枠は C-23 → C-24 のまとまり（P3 の分類待ちと P2 の計画待ち）だけ。畳んだまま、中身は見えない
    const closed = graph.getByRole("button", {
      name: /^ほかの課題を開く（2 件）/,
    });
    await expect(closed).toBeVisible();
    for (const id of ["C-23", "C-24"]) await expect(nodeBtn(id)).toHaveCount(0);
    // 線の無い人間の操作待ち（C-3・C-7）・実行中（C-20）・P0 の分類待ち（C-25）は、大枠に入らずそのまま並ぶ
    for (const id of ["C-3", "C-7", "C-20", "C-25"])
      await expect(nodeBtn(id)).toBeVisible();
    // 2 段以上先の依存の相手: 実施中の C-5 → C-19 → C-2。C-2 は 2 段先で前に出る（大枠の見出し）
    await expect(nodeBtn("C-19")).toBeVisible();
    await expect(
      graph.locator('.graph-group-head[aria-label^="C-2 "]'),
    ).toBeVisible();
    // 注目している親 C-2 の子 C-1 と孫 C-27 は、注目に当たらなくても親の入れ子のまま前に出る
    const c1Open = graph.getByRole("button", { name: /^C-1 を開く/ });
    await expect(c1Open).toBeVisible();
    await c1Open.click();
    await expect(nodeBtn("C-27")).toBeVisible();
    const inside = async (outer: string, id: string) => {
      const o = await graph.locator(outer).first().boundingBox();
      const n = await nodeBtn(id).locator(".graph-node-box").boundingBox();
      return (
        !!o &&
        !!n &&
        n.x >= o.x &&
        n.y >= o.y &&
        n.x + n.width <= o.x + o.width &&
        n.y + n.height <= o.y + o.height
      );
    };
    expect(
      await inside(
        '.graph-group:has(> .graph-group-head[aria-label^="C-2 "]) > .graph-group-box',
        "C-27",
      ),
    ).toBe(true);
    // 大枠を開くと、まとまりの中の線（C-23 → C-24）ごと見える。押しても詳細は開かない
    await closed.click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    for (const id of ["C-23", "C-24"]) await expect(nodeBtn(id)).toBeVisible();
    expect(await inside(".graph-others-frame .graph-group-box", "C-24")).toBe(
      true,
    );
    await expect(
      graph.locator('.graph-edge-hit[aria-label^="依存 C-23 → C-24"]'),
    ).toHaveCount(1);
    await graph.getByRole("button", { name: /^ほかの課題を畳む/ }).click();
    await expect(graph.getByRole("button", { name: /^C-23 / })).toHaveCount(0);
  });

  test("人間の操作待ちのチップ: 区分の記号と帯の語が、題名と見分けられる小さな角丸で、ノードの右上の角に出る。ライト・ダークで読める。実装の語は出ない", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    const node = (id: string) =>
      graph.getByRole("button", { name: new RegExp(`^${id} (?!を)`) });
    const chips = (id: string) => node(id).locator(".graph-human-chip");
    await expect(chips("C-3")).toHaveCount(1);
    await expect(chips("C-3").locator(".graph-human-chip-glyph")).toHaveText(
      "✓",
    );
    await expect(chips("C-3").locator(".graph-human-chip-label")).toHaveText(
      "承認待ち",
    );
    // 承認と不可逆操作は並ぶ（入らない語は切り詰める。全部の語は読み上げの名前にある）
    await expect(chips("C-7")).toHaveCount(2);
    await expect(chips("C-7").nth(0)).toHaveAttribute("data-kind", "approvals");
    await expect(chips("C-7").nth(1)).toHaveAttribute(
      "data-kind",
      "operations",
    );
    await expect(
      chips("C-7").nth(1).locator(".graph-human-chip-glyph"),
    ).toHaveText("!");
    await expect(node("C-7")).toHaveAccessibleName(
      /人間の操作待ち・承認待ち・不可逆操作の承認/,
    );
    // 開いた大枠 C-14 の中の C-8（問いに答える）
    await expect(chips("C-8").locator(".graph-human-chip-glyph")).toHaveText(
      "?",
    );
    await expect(chips("C-8").locator(".graph-human-chip-label")).toHaveText(
      "未回答の問い",
    );
    // 人間の操作待ちでないノードにはチップが無い
    await expect(chips("C-20")).toHaveCount(0);
    await expect(chips("C-5")).toHaveCount(0);

    // ノードの右上の角: 箱の上の端に近く、右の端は箱の右から 6px。題名より上の行にある
    const boxOf = await node("C-7").locator(".graph-node-box").boundingBox();
    const last = await chips("C-7").last().boundingBox();
    const title = await node("C-7").locator(".graph-node-title").boundingBox();
    if (!boxOf || !last || !title) throw new Error("位置が取れない");
    expect(last.y - boxOf.y).toBeLessThanOrEqual(8);
    expect(
      boxOf.x + boxOf.width - (last.x + last.width),
    ).toBeGreaterThanOrEqual(4);
    expect(boxOf.x + boxOf.width - (last.x + last.width)).toBeLessThanOrEqual(
      8,
    );
    expect(last.y + last.height).toBeLessThanOrEqual(title.y + 2);

    // 文字は題名より一段小さい
    const fontSize = (sel: string) =>
      node("C-7")
        .locator(sel)
        .first()
        .evaluate((el) => Number.parseFloat(getComputedStyle(el).fontSize));
    expect(await fontSize(".graph-human-chip text")).toBeLessThan(
      await fontSize(".graph-node-title"),
    );

    // 区分の色が薄く敷かれ、ライト・ダークのどちらでも文字が読める
    for (const scheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      const [fill, text] = await chips("C-3").evaluate((el) => [
        getComputedStyle(el.querySelector("rect") as Element).fill,
        getComputedStyle(el.querySelector("text") as Element).fill,
      ]);
      expect(fill).not.toBe("none");
      expect(
        contrast(fill as string, text as string),
        scheme,
      ).toBeGreaterThanOrEqual(4.5);
    }
    await page.emulateMedia({ colorScheme: "light" });
    const html = await page.content();
    expect(html).not.toMatch(
      /approve_plan|approve_completion|answer_question|approve_operation|next_human_actions/,
    );
  });

  test("人間の操作待ちのチップ: 開いた大枠の見出しでも右上に出て、開閉ボタンとぶつからない", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    // C-3（人間の操作待ち）は着手中の子 C-26 を持つ親。畳んだ箱のチップは開閉ボタンの左にある
    const folded = graph.getByRole("button", { name: /^C-3 (?!を)/ });
    const foldedChip = await folded.locator(".graph-human-chip").boundingBox();
    const foldedToggle = await graph
      .getByRole("button", { name: /^C-3 を開く/ })
      .boundingBox();
    if (!foldedChip || !foldedToggle) throw new Error("位置が取れない");
    expect(foldedChip.x + foldedChip.width).toBeLessThanOrEqual(foldedToggle.x);
    // 開くと、見出しの右上に同じチップが出て、右上の開閉ボタンの左に収まる
    await graph.getByRole("button", { name: /^C-3 を開く/ }).click();
    const headBox = graph.locator('.graph-group-head[aria-label^="C-3 "]');
    await expect(headBox.locator(".graph-human-chip")).toHaveCount(1);
    await expect(headBox.locator(".graph-human-chip-glyph")).toHaveText("✓");
    await expect(headBox.locator(".graph-human-chip-label")).toHaveText(
      "承認待ち",
    );
    const chip = await headBox.locator(".graph-human-chip").boundingBox();
    const toggle = await graph
      .getByRole("button", { name: "C-3 を畳む" })
      .boundingBox();
    const frame = await headBox.locator(".graph-group-head-box").boundingBox();
    if (!chip || !toggle || !frame) throw new Error("位置が取れない");
    expect(chip.x + chip.width).toBeLessThanOrEqual(toggle.x);
    expect(chip.y - frame.y).toBeLessThanOrEqual(8);
  });

  test("詳細の長い文章にだけ「全文を開く」が出る（短い・3 行に収まる文章には出ない）", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    // C-3: 短い論点・複数行でも 3 行に収まる文章だけ
    await graph.getByRole("button", { name: /^C-3 (?!を)/ }).click();
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

  test("ボードから開いた詳細にも待っている理由が出る。前提の行と「衝突のため後回し」の行が別になる", async ({
    page,
  }) => {
    await page.goto("/");
    await page.getByRole("button", { name: "ボード", exact: true }).click();
    // C-19 は前提（C-5・C-17）と衝突（実施中の C-6 に譲って後回し）の両方で待っている
    await page.getByTestId("card-C-19").first().getByRole("button").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    const section = dialog.getByRole("region", { name: "待っている理由" });
    await expect(section.locator("li")).toHaveCount(3);
    await expect(section.locator('li[data-basis="prerequisite"]')).toHaveCount(
      2,
    );
    await expect(section.locator('li[data-basis="conflict"]')).toContainText(
      "衝突のため後回し: C-6",
    );
    await page.keyboard.press("Escape");
    // 待っている理由が無い課題（C-6）には節を出さない
    await page.getByTestId("card-C-6").first().getByRole("button").click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByText("待っている理由")).toHaveCount(0);
  });

  test("衝突の線: 向きのある組は中抜きの矢じり、向きの無い組（C-20 と C-26）は矢じりなしの破線で描かれる", async ({
    page,
  }) => {
    await page.goto("/");
    const graph = page.getByRole("group", { name: "alpha の課題の関係グラフ" });
    await expect(graph).toBeVisible();
    const directed = graph.locator(
      'path.graph-edge[data-basis="conflict"][data-directed="true"]',
    );
    await expect(directed.first()).toHaveAttribute(
      "marker-end",
      "url(#graph-arrow-conflict)",
    );
    const free = graph.locator(
      'path.graph-edge[data-basis="conflict"][data-directed="false"]',
    );
    await expect(free).toHaveCount(1);
    await expect(free).not.toHaveAttribute("marker-end", /.+/);
    // 破線で描かれる（計算後の stroke-dasharray が none でない）
    const dash = await free.evaluate(
      (el) => getComputedStyle(el).strokeDasharray,
    );
    expect(dash).not.toBe("none");
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

  test("終わった課題は既定で隠れ、ほかの課題は注目でない課題だけで既定で畳んでいる。人間の操作待ちの行の右上にはチップが出る", async ({
    page,
  }) => {
    await page.goto("/");
    const list = page.getByRole("list", { name: "課題の一覧（親ごと）" });
    await expect(list).toBeVisible();
    await expect(list.getByRole("button", { name: /^C-22 / })).toHaveCount(0);
    await expect(list.getByRole("button", { name: /^C-21 / })).toBeVisible();
    // 大枠は畳んでいる（人間の操作待ち C-3・C-7 や P0 の C-25 が線無しでいても開かない）。入るのは C-23・C-24 のまとまり
    const others = list.getByRole("button", {
      name: /^ほかの課題を開く（2 件）/,
    });
    await expect(others).toBeVisible();
    for (const id of ["C-23", "C-24"])
      await expect(
        list.getByRole("button", { name: new RegExp(`^${id} (?!を)`) }),
      ).toHaveCount(0);
    // 線の無い実行中・人間の操作待ち・P0 の分類待ちは大枠の外に並ぶ
    for (const id of ["C-20", "C-25"])
      await expect(
        list.getByRole("button", { name: new RegExp(`^${id} `) }),
      ).toBeVisible();
    // 注目は推移的: 2 段先の C-2 と、その子 C-1・孫 C-27 も前に出る（大枠の外）
    for (const id of ["C-2", "C-1"])
      await expect(
        list.getByRole("button", { name: new RegExp(`^${id} (?!を)`) }),
      ).toBeVisible();
    const c1Toggle = list.getByRole("button", { name: /^C-1 を開く/ });
    if ((await c1Toggle.count()) > 0) await c1Toggle.click();
    await expect(
      list.getByRole("button", { name: /^C-27 (?!を)/ }),
    ).toBeVisible();
    await others.click();
    for (const id of ["C-23", "C-24"])
      await expect(
        list.getByRole("button", { name: new RegExp(`^${id} (?!を)`) }),
      ).toBeVisible();
    const row = (id: string) =>
      list.locator(
        `li.graph-list-item:has(> .graph-list-row > button.graph-list-open[aria-label^="${id} "])`,
      );
    const c7 = row("C-7");
    const chips = c7.locator(".graph-list-row .graph-human-chip");
    await expect(chips).toHaveCount(2);
    await expect(chips.nth(0).locator(".graph-human-chip-glyph")).toHaveText(
      "✓",
    );
    await expect(chips.nth(0).locator(".graph-human-chip-label")).toHaveText(
      "承認待ち",
    );
    await expect(chips.nth(1).locator(".graph-human-chip-glyph")).toHaveText(
      "!",
    );
    await expect(chips.nth(1).locator(".graph-human-chip-label")).toHaveText(
      "不可逆操作の承認",
    );
    // 行の右上: 行の右端に近く、行の上の端に近い。文字は題名より小さい
    const rowBox = await c7.locator("> .graph-list-row").boundingBox();
    const last = await chips.last().boundingBox();
    if (!rowBox || !last) throw new Error("位置が取れない");
    expect(rowBox.x + rowBox.width - (last.x + last.width)).toBeLessThanOrEqual(
      12,
    );
    expect(last.y - rowBox.y).toBeLessThanOrEqual(8);
    const fontSize = (sel: string) =>
      c7
        .locator(sel)
        .first()
        .evaluate((el) => Number.parseFloat(getComputedStyle(el).fontSize));
    expect(await fontSize(".graph-human-chip")).toBeLessThan(
      await fontSize(".graph-list-open"),
    );
    expect(await horizontalOverflow(page)).toEqual([]);
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
