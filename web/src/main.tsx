import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App.tsx";
import "./styles.css";

async function start() {
  // 偽の API は開発時の mock モードだけ。リテラルの比較なので、本番のビルドでは分岐ごと消え、
  // 動的 import 先（mock/・write/）はバンドルに入らない（build.test.ts が本番ビルドを検査して守る）。
  if (import.meta.env.MODE === "mock") {
    const { installMock } = await import("./mock/install.ts");
    installMock();
  }
  const root = document.getElementById("root");
  if (root) {
    createRoot(root).render(
      <StrictMode>
        <App />
      </StrictMode>,
    );
  }
}

void start();
