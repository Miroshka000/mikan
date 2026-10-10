// What each page keeps in its URL. Kept apart from the pages: the router checks the search
// of a route before its (lazy) page is loaded, so these must not pull the pages into the
// first chunk.
import type { User } from "../api/client";

export const USER_STATES = ["all", "active", "expiring", "limited", "expired", "disabled"] as const;
export const USER_SOURCES = ["all", "admin", "bot", "trial", "import"] as const;
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

export const SETTINGS_TABS = ["general", "subscription", "page", "routing", "security", "system", "import"] as const;
/** The parts of the sections that have them: the subscription page's editor and the routing. */
export const SETTINGS_PARTS = {
  page: ["look", "brand", "blocks", "apps", "docs", "css"],
} as const;
export type SettingsSearch = { tab: (typeof SETTINGS_TABS)[number]; part?: string };

export const TARIFF_TABS = ["tariffs", "pools", "packages"] as const;
export type TariffsSearch = { tab: (typeof TARIFF_TABS)[number] };

/** Payments open on their history; without a way to take payments yet, on setting one up. */
export const PAYMENT_TABS = ["history", "methods", "rules", "promocodes"] as const;
export type PaymentsSearch = { tab?: (typeof PAYMENT_TABS)[number]; promo?: (typeof PROMO_TABS)[number] };

export const PROMO_TABS = ["codes", "history"] as const;

export const TELEGRAM_TABS = ["connect", "menu", "notify", "infra", "broadcast"] as const;
export type TelegramSearch = { tab: (typeof TELEGRAM_TABS)[number] };
