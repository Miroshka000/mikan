import createClient, { type Middleware } from "openapi-fetch";
import { t, tMaybe } from "../i18n";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];
export type User = Schemas["UserView"];
export type Tariff = Schemas["TariffView"];
export type Inbound = Schemas["InboundView"];
export type Preset = Schemas["Info"];
export type Overview = Schemas["OverviewOutputBody"];
export type TrafficPoint = Schemas["TrafficPoint"];
export type UserState = User["state"];

// The server injects <base href="/<secret>/">; everything is relative to it.
const base = new URL("./", document.baseURI);
export const basePath = base.pathname.replace(/\/$/, "");

let csrfToken = "";
export function setCsrf(token: string) {
  csrfToken = token;
}

export class ApiError extends Error {
  status: number;
  detail: string;
  fields: Record<string, string>;
  /** The value the API sent with a field's error, such as the line of a bad rule. */
  values: Record<string, unknown>;
  /** Every detail's text in the API's order, several per field too (what a node is used by). */
  messages: string[];
  retryAfter: number;

  constructor(status: number, body: unknown, retryAfter = 0) {
    const b = (body ?? {}) as Schemas["ErrorModel"];
    super(b.detail || b.title || `HTTP ${status}`);
    this.status = status;
    this.detail = b.detail ?? "";
    this.retryAfter = retryAfter;
    this.fields = {};
    this.values = {};
    this.messages = [];
    // The API sends codes ("port_in_use") with an optional value; texts live in i18n.
    for (const e of b.errors ?? []) {
      const text = apiMessage(e.message ?? "", e.value);
      this.messages.push(text);
      if (!e.location) continue;
      const field = e.location.replace(/^body\./, "");
      this.fields[field] = text;
      this.values[field] = e.value;
    }
  }
}

/** What a code the server sends (a failed payment, import, backup or source read) says, in words; a code with no text as is. */
export function apiCodeText(code: string): string {
  return tMaybe(`errors.api.${code}`) ?? code;
}

function apiMessage(code: string, value: unknown): string {
  return tMaybe(`errors.api.${code}`, { value: typeof value === "string" || typeof value === "number" ? value : "" }) ?? code;
}

const session: Middleware = {
  onRequest({ request }) {
    if (request.method !== "GET" && request.method !== "HEAD" && csrfToken) {
      request.headers.set("X-CSRF-Token", csrfToken);
    }
    return request;
  },
  onResponse({ response, request }) {
    if (response.status === 401 && !request.url.endsWith("/auth/login")) {
      window.dispatchEvent(new Event("mikan:unauthorized"));
    }
    return response;
  },
};

export const api = createClient<paths>({ baseUrl: base.origin + basePath });
api.use(session);

/** rawApi is fetch with the panel's own headers and errors: for the fork's endpoints that
 * have no place in the generated client (the campaign tags' raw JSON), like api is for the
 * rest. */
export async function rawApi(path: string, init: RequestInit = {}): Promise<any> {
  const headers = new Headers(init.headers);
  if (init.method && init.method !== "GET" && init.method !== "HEAD" && csrfToken) headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(base.origin + basePath + path, { ...init, headers });
  const body = await response.json().catch(() => ({}));
  if (response.status === 401 && !path.endsWith("/auth/login")) {
    window.dispatchEvent(new Event("mikan:unauthorized"));
  }
  if (!response.ok) throw new ApiError(response.status, body, Number(response.headers.get("Retry-After") ?? 0));
  return body;
}

type Result<T> = { data?: T; error?: unknown; response: Response };

export async function unwrap<T>(p: Promise<Result<T>>): Promise<T> {
  let res: Result<T>;
  try {
    res = await p;
  } catch {
    throw new ApiError(0, { detail: "network" });
  }
  if (!res.response.ok) {
    throw new ApiError(res.response.status, res.error, Number(res.response.headers.get("Retry-After") ?? 0));
  }
  return res.data as T;
}

/** Human-readable message for errors the UI does not handle specially. */
export function errorText(e: unknown): string {
  if (!(e instanceof ApiError)) return t("errors.generic");
  if (e.status === 0) return t("errors.network");
  if (e.status === 503 && e.detail === "no_free_slots") return t("errors.noSlots");
  if (e.status === 503) return t("errors.nodeDown");
  if (e.status === 403) return t("errors.forbidden");
  if (e.status === 404) return t("errors.notFound");
  if (e.status === 400 || e.status === 409 || e.status === 422) return Object.values(e.fields)[0] || tMaybe(`errors.api.${e.detail}`) || t("errors.checkInput");
  if (e.status === 429) return t("errors.tooMany", { s: e.retryAfter || 60 });
  // A known code says more than "server error" (502 tg_unreachable, say).
  return (e.detail ? tMaybe(`errors.api.${e.detail}`) : undefined) ?? t("errors.server");
}
