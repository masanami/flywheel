// 失敗は CLI と同じエラーコードの閉集合（仕様 §IF / API「エラーコードと HTTP の状態コード」）。
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

// 承認の画面で、課題が承認待ちでなくなっていること（遷移できない状態・完了などの終わりの状態）を表すコードか。
// 要約の読み込みでも承認の POST でも、内部の英語の文を出さず「承認待ちではない」の案内に寄せる判定に使う。
export function isNotAwaitingError(e: unknown): boolean {
  return (
    e instanceof ApiError &&
    (e.code === "invalid_transition" || e.code === "terminal_state")
  );
}
