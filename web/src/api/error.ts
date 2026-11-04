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
