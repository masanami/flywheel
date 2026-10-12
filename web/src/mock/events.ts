import type { MockStore } from "./store.ts";

// EventSource の偽物。接続はすぐ開き、ストアが変わるたびに workspace_changed を流す。
export function createMockEventSource(store: MockStore): typeof EventSource {
  class MockEventSource {
    onopen: (() => void) | null = null;
    onmessage: ((ev: MessageEvent<string>) => void) | null = null;
    onerror: (() => void) | null = null;
    readonly url: string;
    private off: () => void;
    constructor(url: string) {
      this.url = url;
      this.off = store.subscribe((workspace) => {
        const data = JSON.stringify({ type: "workspace_changed", workspace });
        this.onmessage?.({ data } as MessageEvent<string>);
      });
      setTimeout(() => this.onopen?.(), 0);
    }
    close() {
      this.off();
    }
  }
  return MockEventSource as unknown as typeof EventSource;
}
