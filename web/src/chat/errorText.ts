import { ApiError } from "../api/error.ts";
import { CHAT_ENGINE_MISSING_TEXT } from "./texts.ts";

export function errorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 503) {
    return CHAT_ENGINE_MISSING_TEXT;
  }
  return e instanceof Error ? e.message : String(e);
}
