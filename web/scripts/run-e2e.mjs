// `npm run e2e` の入口。空いているポートを 1 つ選び、FLYWHEEL_E2E_PORT に入れて playwright test を起動する。
// 決まったポートだと、2 つの作業ツリーで同時に流したとき、片方が strictPort で落ちる。
// FLYWHEEL_E2E_PORT が既に設定されていれば、それをそのまま使う（選び直さない。やり直しもしない）。
//
// 空きの確認から Vite が使うまでの間に別の作業ツリーが同じポートを取りうるので、Vite の起動が「ポートの使用中」で
// 失敗したと分かったときに限り、別の空きポートでやり直す（最大 MAX_ATTEMPTS 回）。判定は、出力にポート使用中の文言があり、
// かつ試験が 1 件も走っていない（結果の行が無い）こと。試験の失敗ではやり直さない（再実行で失敗を隠さない）。
import { spawn } from "node:child_process";
import { createServer } from "node:net";

const MAX_ATTEMPTS = 3;
// Vite（--strictPort）の「Port N is already in use」と、Playwright の webServer の事前確認の文言。
const PORT_IN_USE = [
  /Port \d+ is already in use/,
  /is already used, make sure that nothing is running on the port/,
];
// 試験が走った印（list レポーターの 1 件ごとの行と、末尾の集計）。
const TESTS_RAN =
  /^\s*(?:✓|✘|-)\s+\d+\s|^\s*\d+ (?:passed|failed|flaky|skipped)\b/m;

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

let child = null;
// 端末の Ctrl-C 以外（CI の時間切れ等）で止められたとき、playwright と vite の木を孤児にしない。
// 子は新しいプロセスのグループの先頭（detached）なので、グループ全体（-pid）へ合図を送る（macOS・Linux）。
function killGroup(sig) {
  if (!child || child.pid === undefined) return;
  try {
    process.kill(-child.pid, sig);
  } catch {
    try {
      child.kill(sig);
    } catch {
      // すでに終わっている
    }
  }
}
// Playwright は SIGINT を受けると、自分が起動した webServer（vite。別のグループ）を止めてから終わる。SIGTERM のままだと
// vite が残ることがあるので、どの合図も SIGINT として送り、一定時間たっても終わらなければ SIGKILL で止める。
for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, () => {
    killGroup("SIGINT");
    setTimeout(() => killGroup("SIGKILL"), 15_000).unref();
  });
}

// playwright test を 1 回流し、{ code, output } を返す。出力は端末へそのまま流しつつ、判定のために覚える。
function runOnce(env) {
  return new Promise((resolve) => {
    let output = "";
    child = spawn("npx", ["playwright", "test", ...process.argv.slice(2)], {
      stdio: ["inherit", "pipe", "pipe"],
      env,
      detached: true,
    });
    child.stdout.on("data", (d) => {
      process.stdout.write(d);
      output += d;
    });
    child.stderr.on("data", (d) => {
      process.stderr.write(d);
      output += d;
    });
    child.on("error", (e) => {
      console.error(e.message);
      resolve({ code: 1, output });
    });
    child.on("exit", (code, signal) => {
      // 同じグループに孫が残っていれば、グループごと止める。
      killGroup("SIGTERM");
      resolve({ code: code ?? (signal ? 1 : 0), output });
    });
  });
}

function failedOnlyBecausePortWasInUse(code, output) {
  return (
    code !== 0 &&
    PORT_IN_USE.some((re) => re.test(output)) &&
    !TESTS_RAN.test(output)
  );
}

const fixedPort = Boolean(process.env.FLYWHEEL_E2E_PORT);
let result = { code: 1, output: "" };
for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
  const env = { ...process.env };
  if (!fixedPort) env.FLYWHEEL_E2E_PORT = String(await freePort());
  result = await runOnce(env);
  if (fixedPort || !failedOnlyBecausePortWasInUse(result.code, result.output))
    break;
  if (attempt < MAX_ATTEMPTS) {
    console.error(
      `run-e2e: ポート ${env.FLYWHEEL_E2E_PORT} が使えなかったので、別の空きポートでやり直す（${attempt}/${MAX_ATTEMPTS}）`,
    );
  }
}
process.exit(result.code);
