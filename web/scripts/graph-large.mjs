// 大きなまとまりの関係グラフを撮り、描画にかかる時間を測る（flywheel#470）。make check には入れない手動の道具。
//
// 使い方（web/ で）:
//   偽のデータ（mock）:  npx vite --mode mock --port 5288 --strictPort  を立ててから
//     node scripts/graph-large.mjs --base 'http://127.0.0.1:5288/?mock=large&latency=0' \
//       --ws large --compare large-flat --modules --out /tmp/graph-large
//   本物（flywheel serve）:  ストアの写しで serve を立ててから
//     node scripts/graph-large.mjs --base http://127.0.0.1:4319/ --ws <ワークスペース名> --out /tmp/graph-serve
//
// 引数:
//   --base     画面の URL（mock では ?mock=large&latency=0 を付ける）
//   --ws       撮って測るワークスペース
//   --compare  比べるワークスペース（つながりの無いデータ。省略可）
//   --runs     各項目を測る回数（既定 7。中央値を出す）
//   --modules  注目のまとまりとレイアウトの計算を、ブラウザの中で関数を直接呼んで測る（vite の開発サーバのときだけ）
//   --out      写真と結果（results.json）の置き場
//   ブラウザは手元の Google Chrome（PLAYWRIGHT_CHANNEL で変えられる。既定 chrome）。

import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "@playwright/test";

const args = process.argv.slice(2);
const opt = (name, def) => {
  const i = args.indexOf(`--${name}`);
  return i >= 0 ? args[i + 1] : def;
};
const base = opt("base", "http://127.0.0.1:5288/?mock=large&latency=0");
const ws = opt("ws", "large");
const compare = opt("compare", null);
const runs = Number(opt("runs", "7"));
const modules = args.includes("--modules");
const out = opt("out", "/tmp/graph-large");
mkdirSync(out, { recursive: true });

const median = (xs) => {
  const s = [...xs].sort((a, b) => a - b);
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};
const summary = (xs) => ({
  runs: xs.length,
  median_ms: Number(median(xs).toFixed(3)),
  min_ms: Number(Math.min(...xs).toFixed(3)),
  max_ms: Number(Math.max(...xs).toFixed(3)),
});

// ページの読み込みの初めから、描いた箱の数をフレームごとに記録する（初回描画までの時間を出すため）
const FRAME_PROBE = () => {
  window.__frames = [];
  const tick = () => {
    const n = document.querySelectorAll(
      ".graph-node, .graph-group, .graph-list-item",
    ).length;
    window.__frames.push([performance.now(), n]);
    if (performance.now() < 15000) requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
};

const browser = await chromium.launch({
  channel: process.env.PLAYWRIGHT_CHANNEL || "chrome",
});

async function openPage(width, height) {
  const page = await browser.newPage({ viewport: { width, height } });
  await page.addInitScript(FRAME_PROBE);
  return page;
}

async function choose(page, name) {
  const select = page.locator(".graph-ws-picker select");
  await select.waitFor();
  if ((await select.inputValue()) !== name) await select.selectOption(name);
  await page
    .getByRole("heading", { name: `${name} の関係グラフ` })
    .or(page.locator(".graph-list"))
    .first()
    .waitFor();
  await settle(page);
}

// 箱の数が 3 フレーム続けて同じになるまで待つ
const settle = (page) =>
  page.evaluate(
    () =>
      new Promise((resolve) => {
        let last = -1;
        let same = 0;
        const tick = () => {
          const n = document.querySelectorAll(
            ".graph-node, .graph-group, .graph-list-item",
          ).length;
          same = n > 0 && n === last ? same + 1 : 0;
          last = n;
          if (same >= 3) resolve(n);
          else requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      }),
  );

// 操作してから、画面が描き終わる（次のフレームの後）までの時間
const timeAction = (page, selector, how) =>
  page.evaluate(
    ([sel, kind]) =>
      new Promise((resolve, reject) => {
        const el = document.querySelector(sel);
        if (!el) {
          reject(new Error(`見つからない: ${sel}`));
          return;
        }
        const t0 = performance.now();
        if (kind === "hover")
          el.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
        else if (kind === "unhover")
          el.dispatchEvent(new MouseEvent("mouseout", { bubbles: true }));
        else el.click();
        requestAnimationFrame(() =>
          setTimeout(() => resolve(performance.now() - t0), 0),
        );
      }),
    [selector, how],
  );

// 新しく読み込んでから、最終の数の箱を初めて描いたフレームまで（ワークスペースは既定の 1 つ目か、選び直した後）
async function firstRender(width, height, name) {
  const page = await openPage(width, height);
  await page.goto(base);
  await choose(page, name);
  const r = await page.evaluate(() => {
    const final = document.querySelectorAll(
      ".graph-node, .graph-group, .graph-list-item",
    ).length;
    const hit = window.__frames.find(([, n]) => n === final);
    return { ms: hit ? hit[0] : null, boxes: final };
  });
  await page.close();
  return r;
}

async function measureRender(name) {
  const res = {};
  // 初回描画: 1 つ目のワークスペースのときだけ素直に測れる（選び直すと前のワークスペースの描画が混ざる）
  const loads = [];
  let boxes = 0;
  for (let i = 0; i < runs; i++) {
    const r = await firstRender(1440, 900, name);
    if (r.ms !== null) loads.push(r.ms);
    boxes = r.boxes;
  }
  res.first_render_from_navigation = { ...summary(loads), boxes };

  const page = await openPage(1440, 900);
  await page.goto(base);
  await choose(page, name);
  const toggle = 'input[type="checkbox"]';
  const showAll = [];
  const hideAgain = [];
  for (let i = 0; i < runs; i++) {
    showAll.push(await timeAction(page, toggle, "click"));
    await settle(page);
    hideAgain.push(await timeAction(page, toggle, "click"));
    await settle(page);
  }
  res.toggle_show_finished = summary(showAll);
  res.toggle_hide_finished = summary(hideAgain);

  const openAll = [];
  const closeAll = [];
  const btn = (label) =>
    page.evaluate((l) => {
      const b = [...document.querySelectorAll("button")].find(
        (x) => x.textContent?.trim() === l,
      );
      if (!b) return null;
      b.setAttribute("data-measure", l);
      return `button[data-measure="${l}"]`;
    }, label);
  const openSel = await btn("親をすべて開く");
  const closeSel = await btn("親をすべて畳む");
  if (openSel && closeSel) {
    for (let i = 0; i < runs; i++) {
      openAll.push(await timeAction(page, openSel, "click"));
      await settle(page);
      closeAll.push(await timeAction(page, closeSel, "click"));
      await settle(page);
    }
    res.open_all_parents = summary(openAll);
    res.close_all_parents = summary(closeAll);
  }

  // 課題に重ねる（先行・後続の強調で全体を描き直す）
  const hover = [];
  const target = '.graph-node[data-category="running"], .graph-node';
  for (let i = 0; i < runs; i++) {
    hover.push(await timeAction(page, target, "hover"));
    await timeAction(page, target, "unhover");
  }
  res.hover_node = summary(hover);
  await page.close();
  return res;
}

// 注目のまとまり・レイアウトの計算だけを、ブラウザの中で関数を直接呼んで測る（vite の開発サーバのときだけ）
async function measureModules(name) {
  const page = await openPage(1440, 900);
  await page.goto(base);
  await choose(page, name);
  const r = await page.evaluate(
    async ([n, count]) => {
      const m = await import("/src/graph/model.ts");
      const v = await import("/src/graph/GraphView.tsx");
      const s = await import("/src/mock/store.ts");
      const w = s.seedLarge()[n];
      const g = s.graphOf(w);
      const all = new Set(g.nodes.map((x) => x.id));
      const rels = m.validRelations(all, m.relationsOf(g));
      const pm = m.parentMap(all, rels);
      const kids = m.childrenMap(pm);
      // 時計の刻み（Chrome では 0.1 ms 程度）より短いので、1 回の値は 50 回続けて呼んだ時間の平均にする
      const BATCH = 50;
      const t = (fn) => {
        const xs = [];
        for (let i = 0; i < count; i++) {
          const t0 = performance.now();
          for (let j = 0; j < BATCH; j++) fn();
          xs.push((performance.now() - t0) / BATCH);
        }
        return xs;
      };
      const out = {};
      for (const showAll of [false, true]) {
        const ids = m.visibleIds(g.nodes, rels, showAll);
        const others = m.othersOf(ids, rels, g.nodes);
        const isOther = (id) => others.includes(id);
        const head = () => v.WIDE.headH;
        const allOpen = new Set([...kids.keys(), m.OTHERS_ID]);
        const k = showAll ? "all" : "default";
        out[`focusOf_${k}`] = t(() => m.focusOf(ids, rels, g.nodes));
        out[`othersOf_${k}`] = t(() => m.othersOf(ids, rels, g.nodes));
        out[`layout_x2_closed_${k}`] = t(() => {
          m.layoutGraph(ids, rels, v.WIDE, new Set(), head, isOther);
          m.layoutGraph(ids, rels, v.WIDE, new Set(), head, isOther);
        });
        out[`layout_x2_all_open_${k}`] = t(() => {
          m.layoutGraph(ids, rels, v.WIDE, allOpen, head, isOther);
          m.layoutGraph(ids, rels, v.WIDE, allOpen, head, isOther);
        });
        out[`shown_${k}`] = [ids.length];
        out[`others_${k}`] = [others.length];
      }
      return out;
    },
    [name, Math.max(runs, 20)],
  );
  await page.close();
  const res = {};
  for (const [k, xs] of Object.entries(r))
    res[k] =
      k.startsWith("shown_") || k.startsWith("others_") ? xs[0] : summary(xs);
  return res;
}

// 中の入れ物でスクロールする分まで写るよう、ビューポートを中身の高さまで伸ばしてから撮る
async function fullShot(page, path) {
  const size = page.viewportSize();
  const h = await page.evaluate(() =>
    Math.max(...[...document.querySelectorAll("*")].map((e) => e.scrollHeight)),
  );
  await page.setViewportSize({
    width: size.width,
    height: Math.min(Math.max(h + 120, size.height), 12000),
  });
  await settle(page);
  await page.screenshot({ path, fullPage: true });
  await page.setViewportSize(size);
  await settle(page);
}

async function shoot(name, tag) {
  const wide = await openPage(1440, 900);
  await wide.goto(base);
  await choose(wide, name);
  await fullShot(wide, join(out, `${tag}-1440-graph.png`));
  const btn = wide.getByRole("button", { name: "親をすべて開く" });
  if (await btn.count()) {
    await btn.click();
    await settle(wide);
    await fullShot(wide, join(out, `${tag}-1440-graph-all-open.png`));
    await wide.getByRole("button", { name: "親をすべて畳む" }).click();
    await settle(wide);
  }
  await wide.locator("label.graph-switch").click();
  await settle(wide);
  await fullShot(wide, join(out, `${tag}-1440-graph-show-finished.png`));
  await wide.close();

  const narrow = await openPage(390, 844);
  await narrow.goto(base);
  await choose(narrow, name);
  await fullShot(narrow, join(out, `${tag}-390-list.png`));
  await narrow.close();
}

const result = {
  base,
  ws,
  compare,
  runs,
  env: {
    userAgent: await (async () => {
      const p = await browser.newPage();
      const ua = await p.evaluate(() => navigator.userAgent);
      await p.close();
      return ua;
    })(),
    node: process.version,
    platform: `${process.platform} ${process.arch}`,
  },
  measures: {},
};
for (const [name, tag] of [
  [ws, "main"],
  ...(compare ? [[compare, "compare"]] : []),
]) {
  await shoot(name, tag);
  result.measures[name] = {
    render: await measureRender(name),
    ...(modules ? { compute: await measureModules(name) } : {}),
  };
}
await browser.close();
writeFileSync(join(out, "results.json"), JSON.stringify(result, null, 2));
console.log(JSON.stringify(result, null, 2));
