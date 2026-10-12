import { setApi } from "../api/client.ts";
import { proposalScreens } from "../proposal/screens.tsx";
import { registerScreens } from "../screens.tsx";
import { createMockApi } from "./api.ts";
import { createMockEventSource } from "./events.ts";
import { MockStore } from "./store.ts";

// 開発時の mock モード（`npm run dev:mock`）だけが呼ぶ。偽の API・偽の EventSource・UX 案の画面を入れる。
export function installMock(): void {
  const store = new MockStore();
  setApi(createMockApi(store));
  window.EventSource = createMockEventSource(store);
  registerScreens(proposalScreens);
}
