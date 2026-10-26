// What each page keeps in its URL. Kept apart from the pages: the router checks the search
// of a route before its (lazy) page is loaded, so these must not pull the pages into the
// first chunk.
import type { User } from "../api/client";

export const USER_STATES = ["all", "active", "expiring", "limited", "expired", "disabled"] as const;
export const USER_SOURCES = ["all", "admin", "bot", "trial", "free", "import"] as const;
/** Hidden users: left off the list, listed with the rest, or the only ones listed. */
export const USER_HIDDEN = ["hide", "show", "only"] as const;
/**
 * The filters of the users list live in the URL, so a reload or a shared link keeps them.
 * What is the default (any folder, any source, hidden left off) is left out of it.
 * folder: "none" (outside folders) or a folder's id.
 */
export type UsersSearch = {
  state: "all" | User["state"];
  q: string;
  user?: number;
  create?: true;
  folder?: number | "none";
  source?: Exclude<(typeof USER_SOURCES)[number], "all">;
  hidden?: Exclude<(typeof USER_HIDDEN)[number], "hide">;
};

export const SETTINGS_TABS = ["general", "subscription", "page", "routing", "security", "support", "system", "import"] as const;
/** The parts of the sections that have them: the subscription (its settings, its apps, how it answers requests, its templates), the subscription page's editor and the routing. */
export const SETTINGS_PARTS = {
  subscription: ["main", "clients", "responses", "templates"],
  page:["look", "brand", "blocks", "apps", "docs", "css"],
  routing: ["mode", "lists", "rules", "dns", "advanced"],
} as const;
export type SettingsSearch = { tab: (typeof SETTINGS_TABS)[number]; part?: string };

export const TARIFF_TABS = ["tariffs", "pools", "packages"] as const;
export type TariffsSearch = { tab: (typeof TARIFF_TABS)[number] };

/** Payments open on their history; without a way to take payments yet, on setting one up. */
export const PAYMENT_TABS = ["history", "topups", "methods", "rules", "promocodes", "campaigns"] as const;
export type PaymentsSearch = { tab?: (typeof PAYMENT_TABS)[number]; promo?: (typeof PROMO_TABS)[number] };

export const PROMO_TABS = ["codes", "history"] as const;

export const TELEGRAM_TABS = ["connect", "menu", "notify", "infra", "broadcast"] as const;
export type TelegramSearch = { tab: (typeof TELEGRAM_TABS)[number] };

/** The addons open on what is installed; the catalog and the community keep their search and filter by kind. */
export const ADDON_TABS = ["installed", "catalog", "community"] as const;
export const ADDON_KINDS = ["bot", "payment", "ui"] as const;
export type AddonKind = (typeof ADDON_KINDS)[number];
export type AddonsSearch = { tab?: (typeof ADDON_TABS)[number]; q?: string; kind?: AddonKind };

/** Diagnostics: the logs, the server check with the restart, and (for support) the terminal. */
export const DIAG_TABS = ["logs", "check", "reach", "geo", "terminal"] as const;
export type DiagSearch = { tab?: (typeof DIAG_TABS)[number]; source?: string };

/** The operator's entry page: `ended` after a link that stopped working under them. */
export type SupportSearch = { ended?: true };
