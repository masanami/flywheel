import { setApi } from "../api/client.ts";
import { registerScreens } from "../screens.tsx";
import { writeScreens } from "../write/screens.tsx";
import { createMockApi } from "./api.ts";
import { createMockEventSource } from "./events.ts";
import { mockScreens } from "./screens.tsx";
import { MockStore } from "./store.ts";

// 開発時の mock モード（`npm run dev:mock`）だけが呼ぶ。偽の API・偽の EventSource・書き込みと S4 の画面を入れる。
export function installMock(): void {
  const store = new MockStore();
  setApi(createMockApi(store));
  window.EventSource = createMockEventSource(store);
  registerScreens([...writeScreens, ...mockScreens(store)]);
}
