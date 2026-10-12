import { mkdtempSync, readFileSync, readdirSync, rmSync } from "node:fs";
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

// 本番のビルド（flywheel serve に埋め込む）に、偽の API と書き込み・S4 の画面が入らないこと。
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
      "モックの操作",
      "通知メールの二重送信を止める",
      "まとめて承認",
      "mock-passkey",
    ]) {
      expect(code, marker).not.toContain(marker);
    }
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}, 60_000);
