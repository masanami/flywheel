import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

// 本物の serve の E2E の準備の段（flywheel#463）。
//   1. 一時ディレクトリに HOME・XDG_CONFIG_HOME・作業ディレクトリ・ワークスペース（seed）を作る
//   2. Go のテスト TestRelationGraphSeed で、ワークスペースへ種のストアを毎回作る
//   3. ビルド済みの build/flywheel を `serve --port 0 --workspace <ワークスペース>` で起動し、
//      標準エラーの `listening on <addr>` から番地を読んで、試験へ環境変数で渡す
// 一時の HOME・XDG_CONFIG_HOME は serve にだけ渡す（Go のキャッシュと Playwright のブラウザの置き場は
// 元の場所のまま）。委譲の目印（FLYWHEEL_DELEGATED_RUN）は外さない。目印のある環境では serve が拒否され、
// ここで失敗する（想定どおり）。返す関数が終了時の片づけ（serve を止め、一時ディレクトリを消す）。

const repoRoot = resolve(import.meta.dirname, "..", "..");
const binary = join(repoRoot, "build", "flywheel");
export const BASE_URL_ENV = "FLYWHEEL_E2E_REAL_BASE_URL";
const SEED_ENV = "FLYWHEEL_E2E_SEED_WORKSPACE";

function waitForListening(serve: ChildProcess): Promise<string> {
  return new Promise((ok, fail) => {
    let err = "";
    const timer = setTimeout(
      () => fail(new Error(`serve が 30 秒で待ち受けを始めない:\n${err}`)),
      30_000,
    );
    serve.stderr?.on("data", (chunk: Buffer) => {
      err += chunk.toString();
      const m = err.match(/listening on (\S+)/);
      if (m?.[1]) {
        clearTimeout(timer);
        ok(m[1]);
      }
    });
    serve.on("exit", (code) => {
      clearTimeout(timer);
      fail(new Error(`serve が終了した（終了コード ${code}）:\n${err}`));
    });
  });
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  if (!existsSync(binary)) {
    throw new Error(
      `${binary} が無い。make e2e-real（ui-build と build の後に流す）から起動する`,
    );
  }
  const root = mkdtempSync(join(tmpdir(), "flywheel-e2e-real-"));
  const home = join(root, "home");
  const xdg = join(root, "xdg-config");
  const cwd = join(root, "cwd");
  const workspace = join(root, "seed");
  for (const d of [home, xdg, cwd]) mkdirSync(d, { recursive: true });

  execFileSync(
    "go",
    ["test", "-count=1", "-run", "^TestRelationGraphSeed$", "./internal/core/"],
    {
      cwd: repoRoot,
      env: { ...process.env, [SEED_ENV]: workspace },
      stdio: "inherit",
    },
  );

  const serve = spawn(
    binary,
    ["serve", "--port", "0", "--workspace", workspace],
    {
      cwd,
      env: { ...process.env, HOME: home, XDG_CONFIG_HOME: xdg },
      stdio: ["ignore", "inherit", "pipe"],
    },
  );
  let addr: string;
  try {
    addr = await waitForListening(serve);
  } catch (e) {
    serve.kill("SIGTERM");
    rmSync(root, { recursive: true, force: true });
    throw e;
  }
  serve.stderr?.on("data", (chunk: Buffer) => process.stderr.write(chunk));
  process.env[BASE_URL_ENV] = `http://${addr}`;

  return async () => {
    if (serve.exitCode === null) {
      const exited = new Promise((ok) => serve.once("exit", ok));
      serve.kill("SIGTERM");
      await exited;
    }
    rmSync(root, { recursive: true, force: true });
  };
}
