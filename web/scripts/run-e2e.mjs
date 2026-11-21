// `npm run e2e` の入口。空いているポートを 1 つ選び、FLYWHEEL_E2E_PORT に入れて playwright test を起動する。
// 決まったポートだと、2 つの作業ツリーで同時に流したとき、片方が strictPort で落ちる。
// FLYWHEEL_E2E_PORT が既に設定されていれば、それをそのまま使う（選び直さない）。
import { spawn } from "node:child_process";
import { createServer } from "node:net";

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

const env = { ...process.env };
if (!env.FLYWHEEL_E2E_PORT) {
  env.FLYWHEEL_E2E_PORT = String(await freePort());
}
const child = spawn("npx", ["playwright", "test", ...process.argv.slice(2)], {
  stdio: "inherit",
  env,
});
child.on("error", (e) => {
  console.error(e.message);
  process.exit(1);
});
// 端末の Ctrl-C 以外（CI の時間切れ等）で止められたとき、playwright と vite の木を孤児にしない。
for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, () => child.kill(sig));
}
child.on("exit", (code, signal) => {
  process.exit(code ?? (signal ? 1 : 0));
});
