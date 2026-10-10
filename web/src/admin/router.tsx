import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet, redirect, type AsyncRouteComponent } from "@tanstack/react-router";
import { ApiError, basePath, setCsrf } from "../api/client";
import { meQuery } from "../api/hooks";
import { useLocale } from "../i18n";
import { NotFoundPage, PageLoading, RouteError } from "./route-states";
import { PAYMENT_TABS, PROMO_TABS, SETTINGS_PARTS, SETTINGS_TABS, TARIFF_TABS, TELEGRAM_TABS, USER_SOURCES, USER_STATES, type PaymentsSearch, type SettingsSearch, type TariffsSearch, type TelegramSearch, type UsersSearch } from "./search";
import { Shell } from "./shell";

/**
 * A page loaded on demand: the first screen (login, the shell) does not carry the code of
 * every page. The page subscribes to the language itself, so a switch redraws it with its
 * form state and open drawers intact (texts are read at render time).
 */
function page<M extends Record<string, unknown>, K extends keyof M & string>(load: () => Promise<M>, name: K) {
  const Lazy = lazyRouteComponent(load, name) as AsyncRouteComponent<object>;
  const Page = () => {
    useLocale();
    return <Lazy />;
  };
  // The router warms the chunk up when a link is hovered (defaultPreload: "intent").
  return Object.assign(Page, { preload: Lazy.preload });
}

export function createAppRouter(queryClient: QueryClient) {
  const root = createRootRouteWithContext<{ queryClient: QueryClient }>()({ component: () => <Outlet /> });

  const login = createRoute({
    getParentRoute: () => root,
    path: "/login",
    component: page(() => import("./pages/login"), "LoginPage"),
    validateSearch: (s: Record<string, unknown>): { next?: string } => ({ next: typeof s.next === "string" ? s.next : undefined }),
  });

  const app = createRoute({
    getParentRoute: () => root,
    id: "_app",
    component: Shell,
    beforeLoad: async ({ context, location }) => {
      try {
        const me = await context.queryClient.ensureQueryData(meQuery);
        setCsrf(me.csrf_token);
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) {
          throw redirect({ to: "/login", search: { next: location.href } });
        }
        throw e;
      }
    },
  });

  const dashboard = createRoute({ getParentRoute: () => app, path: "/", component: page(() => import("./pages/dashboard"), "Dashboard") });
  const users = createRoute({
    getParentRoute: () => app,
    path: "/users",
    component: page(() => import("./pages/users"), "UsersPage"),
    validateSearch: (s: Record<string, unknown>): UsersSearch => ({
      state: USER_STATES.includes(s.state as (typeof USER_STATES)[number]) ? (s.state as UsersSearch["state"]) : "all",
      q: typeof s.q === "string" ? s.q : "",
      user: typeof s.user === "number" ? s.user : Number(s.user) || undefined,
      create: s.create === true || s.create === "true" ? true : undefined,
      // A folder is "none" or an id; anything else in a hand-made link is dropped.
      folder: s.folder === "none" ? "none" : (typeof s.folder === "number" || typeof s.folder === "string") && Number.isSafeInteger(Number(s.folder)) && Number(s.folder) >= 1 ? Number(s.folder) : undefined,
      source: USER_SOURCES.includes(s.source as (typeof USER_SOURCES)[number]) && s.source !== "all" ? (s.source as UsersSearch["source"]) : undefined,
      hidden: s.hidden === "show" || s.hidden === "only" ? s.hidden : undefined,
    }),
  });
  const tariffs = createRoute({
    getParentRoute: () => app,
    path: "/tariffs",
    component: page(() => import("./pages/tariffs"), "TariffsPage"),
    validateSearch: (s: Record<string, unknown>): TariffsSearch => ({
      tab: TARIFF_TABS.includes(s.tab as TariffsSearch["tab"]) ? (s.tab as TariffsSearch["tab"]) : "tariffs",
    }),
  });
  const inbounds = createRoute({ getParentRoute: () => app, path: "/inbounds", component: page(() => import("./pages/inbounds"), "InboundsPage") });
  const nodes = createRoute({ getParentRoute: () => app, path: "/nodes", component: page(() => import("./pages/nodes"), "NodesPage") });
  const settings = createRoute({
    getParentRoute: () => app,
    path: "/settings",
    component: page(() => import("./pages/settings"), "SettingsPage"),
    // The torrent blocker was a part of the routing until it became an addon.
    beforeLoad: ({ search }) => {
      if (search.tab === "routing" && search.part === "torrent") throw redirect({ to: "/addons/torrent" });
    },
    validateSearch: (s: Record<string, unknown>): SettingsSearch => {
      // Sections that moved keep their old links working: the Clash rules are a field of the
      // simple routing now, the torrent blocker is in Security.
      if (s.tab === "rules" || (s.tab === "routing" && s.part === "rules")) return { tab: "routing", part: "simple" };
      if (s.tab === "routing" && s.part === "torrent") return { tab: "routing", part: "torrent" }; // to the addons, below
      const tab = SETTINGS_TABS.includes(s.tab as SettingsSearch["tab"]) ? (s.tab as SettingsSearch["tab"]) : "general";
      // The routing has no parts in the list beside it, but its view is in the URL.
      if (tab === "routing") return { tab, part: s.part === "simple" || s.part === "yaml" ? s.part : undefined };
      const parts: readonly string[] | undefined = (SETTINGS_PARTS as Record<string, readonly string[]>)[tab];
      return { tab, part: parts && typeof s.part === "string" && parts.includes(s.part) ? s.part : undefined };
    },
  });
  // The addons: the built-in tools (the Telegram bot, the torrent blocker) and the
  // marketplace's payment methods.
  const addons = createRoute({
    getParentRoute: () => app,
    path: "/addons",
    component: page(() => import("./pages/addons"), "AddonsPage"),
  });
  const telegram = createRoute({
    getParentRoute: () => app,
    path: "/addons/telegram",
    component: page(() => import("./pages/telegram"), "TelegramPage"),
    validateSearch: (s: Record<string, unknown>): TelegramSearch => ({
      tab: TELEGRAM_TABS.includes(s.tab as TelegramSearch["tab"]) ? (s.tab as TelegramSearch["tab"]) : "connect",
    }),
  });
  const torrent = createRoute({
    getParentRoute: () => app,
    path: "/addons/torrent",
    component: page(() => import("./pages/addons"), "TorrentPage"),
  });
  const filters = createRoute({
    getParentRoute: () => app,
    path: "/addons/filters",
    component: page(() => import("./pages/filters"), "FiltersPage"),
  });
  // The bot was a section of the menu until it moved to the addons: old links still work.
  const telegramOld = createRoute({
    getParentRoute: () => app,
    path: "/telegram",
    beforeLoad: ({ search }) => {
      throw redirect({ to: "/addons/telegram", search: search as TelegramSearch });
    },
  });
  // Promo codes were a page of their own until 0.5.0.5: old links land on their tab in Payments.
  const promocodes = createRoute({
    getParentRoute: () => app,
    path: "/promocodes",
    beforeLoad: ({ search }) => {
      const tab = (search as { tab?: string }).tab;
      throw redirect({ to: "/payments", search: { tab: "promocodes", promo: PROMO_TABS.includes(tab as (typeof PROMO_TABS)[number]) ? (tab as (typeof PROMO_TABS)[number]) : undefined } });
    },
  });
  const payments = createRoute({
    getParentRoute: () => app,
    path: "/payments",
    component: page(() => import("./pages/payments"), "PaymentsPage"),
    validateSearch: (s: Record<string, unknown>): PaymentsSearch => ({
      tab: PAYMENT_TABS.includes(s.tab as (typeof PAYMENT_TABS)[number]) ? (s.tab as PaymentsSearch["tab"]) : undefined,
      promo: PROMO_TABS.includes(s.promo as (typeof PROMO_TABS)[number]) ? (s.promo as PaymentsSearch["promo"]) : undefined,
    }),
  });
  const apiDocs = createRoute({ getParentRoute: () => app, path: "/settings/api", component: page(() => import("./pages/api"), "ApiPage") });
  // The API section lived in the sidebar until 0.4.2: old links land on its new place.
  const apiDocsOld = createRoute({ getParentRoute: () => app, path: "/api-docs", beforeLoad: () => { throw redirect({ to: "/settings/api" }); } });

  const routeTree = root.addChildren([login, app.addChildren([dashboard, users, tariffs, inbounds, nodes, promocodes, payments, addons, telegram, torrent, filters, telegramOld, apiDocs, apiDocsOld, settings])]);
  return createRouter({
    routeTree,
    basepath: basePath || "/",
    context: { queryClient },
    defaultPreload: "intent",
    scrollRestoration: true,
    defaultErrorComponent: RouteError,
    defaultNotFoundComponent: NotFoundPage,
    // A page's code is usually cached after the first visit: the placeholder shows only
    // when loading really takes a moment, and then stays long enough not to flash.
    defaultPendingComponent: PageLoading,
    defaultPendingMs: 200,
    defaultPendingMinMs: 300,
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
