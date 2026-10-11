import { vi } from "vitest";

export class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  url: string;
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
  open() {
    this.onopen?.();
  }
  send(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent<string>);
  }
  fail() {
    this.onerror?.();
  }
  static get latest(): FakeEventSource {
    const e = FakeEventSource.instances.at(-1);
    if (!e) throw new Error("EventSource がまだ作られていない");
    return e;
  }
}

const emptyStatus = {
  actionable: { challenges: [] },
  approved: { operations: [] },
  needs_human: {
    budget_exhausted: [],
    challenges: [],
    discrepancies: [],
    operations: [],
    slots: [],
    triage: [],
  },
  waiting_external: { challenges: [] },
};

export function fixtures(): Record<string, unknown> {
  const ws = (name: string) => ({
    error: null,
    name,
    path: `/w/${name}`,
    state: "ok",
  });
  return {
    "/api/v1/workspaces": { workspaces: [ws("alpha"), ws("beta")] },
    "/api/v1/workspaces/alpha/status": emptyStatus,
    "/api/v1/workspaces/alpha/challenges": { challenges: [] },
    "/api/v1/workspaces/beta/status": emptyStatus,
    "/api/v1/workspaces/beta/challenges": { challenges: [] },
    "/api/v1/needs-human": { unreadable_workspaces: [], workspaces: [] },
    "/api/v1/questions": { questions: [], unreadable_workspaces: [] },
  };
}

export interface Recorded {
  url: string;
  method: string;
}

// fetch と EventSource を偽物に差し替え、発行した要求を記録する。
export function installFakes(bodies: Record<string, unknown> = fixtures()) {
  const requests: Recorded[] = [];
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string | Request, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      const method = (
        init?.method ?? (input instanceof Request ? input.method : "GET")
      ).toUpperCase();
      requests.push({ url, method });
      const body = bodies[url];
      if (body === undefined) return new Response("{}", { status: 404 });
      return new Response(JSON.stringify(body), { status: 200 });
    }),
  );
  return { requests };
}
