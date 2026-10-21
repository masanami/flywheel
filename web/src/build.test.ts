import { mkdtempSync, readFileSync, readdirSync, rmSync } from "node:fs";
import { existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { build } from "vite";
import { expect, it } from "vitest";

function jsFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory()
      ? jsFiles(join(dir, e.name))
      : e.name.endsWith(".js")
        ? [join(dir, e.name)]
        : [],
  );
}

// 本番のビルド（flywheel serve に埋め込む）に、偽の API と UX 案の画面が入らないこと。
it("本番のビルドに偽の API・モックの画面を含めない", async () => {
  const out = mkdtempSync(join(tmpdir(), "flywheel-web-build-"));
  try {
    await build({
      logLevel: "silent",
      build: { outDir: out, emptyOutDir: true },
    });
    const files = jsFiles(out);
    expect(files.length).toBeGreaterThan(0);
    const code = files.map((f) => readFileSync(f, "utf8")).join("\n");
    // 画面の文言・モックのデータの文字列は、ミニファイしても残る
    for (const marker of [
      "ボード（案）",
      "人間の操作待ち（案）",
      "通知メールの二重送信を止める",
      "優先度方針: 厳格",
      "関係グラフ（案）",
      "関係の無い課題",
      "親をすべて開く",
      "旧基盤の撤去",
      "mock-org/",
    ]) {
      expect(code, marker).not.toContain(marker);
    }
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}, 60_000);

it("本番のビルドは manifest を出さない（D3）", async () => {
  const out = mkdtempSync(join(tmpdir(), "flywheel-web-build-"));
  try {
    await build({
      logLevel: "silent",
      build: { outDir: out, emptyOutDir: true },
    });
    expect(existsSync(join(out, ".vite", "manifest.json"))).toBe(false);
    expect(existsSync(join(out, "manifest.json"))).toBe(false);
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}, 60_000);

// 同じ origin の相対パスか（スキームも // も持たない）。JS の本文中の URL 文字列は検べない。
const sameOriginRelative = (u: string) =>
  u !== "" && !/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(u) && !u.startsWith("//");

it("ビルド成果物の参照は同じ origin の相対パスだけ・Mermaid は別チャンク（AC-391・AC-381）", async () => {
  const out = mkdtempSync(join(tmpdir(), "flywheel-web-build-"));
  try {
    // manifest は本番のビルドでは出さない。この検査のためのビルドでだけ出す。
    await build({
      logLevel: "silent",
      build: { outDir: out, emptyOutDir: true, manifest: true },
    });
    const html = readFileSync(join(out, "index.html"), "utf8");
    const refs = [
      ...[...html.matchAll(/<script\b[^>]*\bsrc="([^"]*)"/g)].map(
        (m) => m[1] ?? "",
      ),
      ...[...html.matchAll(/<link\b[^>]*\bhref="([^"]*)"/g)].map(
        (m) => m[1] ?? "",
      ),
    ];
    expect(refs.length).toBeGreaterThan(0);
    for (const r of refs) expect(sameOriginRelative(r), r).toBe(true);

    const manifestPath = [".vite/manifest.json", "manifest.json"]
      .map((p) => join(out, p))
      .find((p) => existsSync(p));
    expect(manifestPath).toBeDefined();
    const manifest = JSON.parse(
      readFileSync(manifestPath as string, "utf8"),
    ) as Record<
      string,
      {
        file: string;
        imports?: string[];
        dynamicImports?: string[];
        isEntry?: boolean;
      }
    >;
    for (const [key, e] of Object.entries(manifest)) {
      expect(sameOriginRelative(e.file), e.file).toBe(true);
      for (const i of [...(e.imports ?? []), ...(e.dynamicImports ?? [])]) {
        expect(sameOriginRelative(i), `${key} -> ${i}`).toBe(true);
        expect(manifest[i], `${key} -> ${i}`).toBeDefined();
      }
    }
    // 初期の画面（入口とその静的な import の推移）に Mermaid を含めず、動的 import の先に分かれている。
    const entry = Object.values(manifest).find((e) => e.isEntry);
    expect(entry).toBeDefined();
    const isMermaid = (k: string) => k.includes("node_modules/mermaid/");
    const dynKeys = entry?.dynamicImports ?? [];
    expect(dynKeys.some(isMermaid)).toBe(true);
    const seen = new Set<string>();
    const walk = (k: string) => {
      if (seen.has(k)) return;
      seen.add(k);
      for (const i of manifest[k]?.imports ?? []) walk(i);
    };
    for (const [k, e] of Object.entries(manifest)) if (e.isEntry) walk(k);
    for (const k of seen) expect(isMermaid(k), k).toBe(false);
    expect(html).not.toMatch(/mermaid/i);
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}, 120_000);
