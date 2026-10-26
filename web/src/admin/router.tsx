import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet, redirect, type AsyncRouteComponent } from "@tanstack/react-router";
import { ApiError, basePath, setCsrf } from "../api/client";
import { meQuery } from "../api/hooks";
import { whoamiQuery } from "../api/support";
import { useLocale } from "../i18n";
import { NotFoundPage, PageLoading, RouteError } from "./route-states";
import { ADDON_KINDS, ADDON_TABS, PAYMENT_TABS, PROMO_TABS, DIAG_TABS, SETTINGS_PARTS, SETTINGS_TABS, TARIFF_TABS, TELEGRAM_TABS, USER_SOURCES, USER_STATES, type AddonsSearch, type DiagSearch, type PaymentsSearch, type SettingsSearch, type SupportSearch, type TariffsSearch, type TelegramSearch, type UsersSearch } from "./search";
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

  // Where a support operator comes in by the link the admin gave them (the token is in the
  // address after #, so it never reaches a server log).
  const supportEnter = createRoute({
    getParentRoute: () => root,
    path: "/support",
    component: page(() => import("./pages/support-enter"), "SupportEnterPage"),
    validateSearch: (s: Record<string, unknown>): SupportSearch => ({ ended: s.ended === true || s.ended === "true" ? true : undefined }),
  });

  const app = createRoute({
    getParentRoute: () => root,
    id: "_app",
    component: Shell,
    beforeLoad: async ({ context, location }) => {
      // Known as a support operator already: the admin's own data is not asked again.
      if (context.queryClient.getQueryData(whoamiQuery.queryKey)) return;
      try {
        const me =await context.queryClient.ensureQueryData(meQuery);
        setCsrf(me.csrf_token);
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) {
          throw redirect({ to: "/login", search: { next: location.href } });
        }
        // The admin's own data is closed to a support operator (403): they are known by their
        // own session, which says what they may do.
        if (e instanceof ApiError && e.status === 403) {
          try {
            const who = await context.queryClient.ensureQueryData(whoamiQuery);
            setCsrf(who.csrf_token ?? "");
            return;
          } catch (e2) {
            if (e2 instanceof ApiError && e2.status === 401) throw redirect({ to: "/support", search: { ended: true } });
            throw e2;
          }
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
  const diagnostics = createRoute({
    getParentRoute: () => app,
    path: "/diagnostics",
    component: page(() => import("./pages/diagnostics"), "DiagnosticsPage"),
    validateSearch: (s: Record<string, unknown>): DiagSearch => ({
      tab: DIAG_TABS.includes(s.tab as (typeof DIAG_TABS)[number]) && s.tab !== "logs" ? (s.tab as DiagSearch["tab"]) : undefined,
      source: typeof s.source === "string" && /^(panel|node:\d{1,9})$/.test(s.source) && s.source !== "panel" ? s.source : undefined,
    }),
  });
  const settings = createRoute({
    getParentRoute: () => app,
    path: "/settings",
    component: page(() => import("./pages/settings"), "SettingsPage"),
    // The torrent blocker was a part of the routing until it became an addon.
    beforeLoad: ({ search }) => {
      if (search.tab === "routing" && search.part === "torrent") throw redirect({ to: "/addons/torrent" });
    },
    validateSearch: (s: Record<string, unknown>): SettingsSearch => {
      // Sections that moved keep their old links working: the Clash rules are a part of the
      // routing now, its old simple view is the mode, the own YAML is a subscription
      // template, the torrent blocker is an addon (its link goes on in beforeLoad).
      if (s.tab === "rules") return { tab: "routing", part: "rules" };
      if (s.tab === "routing" && s.part === "simple") return { tab: "routing", part: "mode" };
      if (s.tab === "routing" && s.part === "yaml") return { tab: "subscription", part: "templates" };
      if (s.tab === "routing" && s.part === "torrent") return { tab: "routing", part: "torrent" };
      const tab = SETTINGS_TABS.includes(s.tab as SettingsSearch["tab"]) ? (s.tab as SettingsSearch["tab"]) : "general";
      const parts: readonly string[] | undefined = (SETTINGS_PARTS as Record<string, readonly string[]>)[tab];
      return { tab, part: parts && typeof s.part === "string" && parts.includes(s.part) ? s.part : undefined };
    },
  });
  // The addons: what is installed (the bot, payment methods, pages, the built-in tools) and
  // the catalog of the marketplace.
  const addons = createRoute({
    getParentRoute: () => app,
    path: "/addons",
    component: page(() => import("./pages/addons"), "AddonsPage"),
    validateSearch: (s: Record<string, unknown>): AddonsSearch => ({
      tab: ADDON_TABS.includes(s.tab as (typeof ADDON_TABS)[number]) && s.tab !== "installed" ? (s.tab as AddonsSearch["tab"]) : undefined,
      q: typeof s.q === "string" && s.q.trim() ? s.q.slice(0, 100) : undefined,
      kind: ADDON_KINDS.includes(s.kind as (typeof ADDON_KINDS)[number]) ? (s.kind as AddonsSearch["kind"]) : undefined,
    }),
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
    component: page(() => import("./pages/torrent-page"), "TorrentPage"),
  });
  const filters = createRoute({
    getParentRoute: () => app,
    path: "/addons/filters",
    component: page(() => import("./pages/filters"), "FiltersPage"),
  });
  // The marketplace had a page of its own until 0.5.1.0: old links land on the catalog.
  const market = createRoute({
    getParentRoute: () => app,
    path: "/addons/market",
    beforeLoad: () => {
      throw redirect({ to: "/addons", search: { tab: "catalog" } });
    },
  });
  // An addon's own page (protocol 2, permission ui), framed in a sandbox.
  const addonApp = createRoute({
    getParentRoute: () => app,
    path: "/addons/app/$id",
    component: page(() => import("./pages/addon-app"), "AddonAppPage"),
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
  const webhooks = createRoute({ getParentRoute: () => app, path: "/settings/webhooks", component: page(() => import("./pages/webhooks"), "WebhooksPage") });
  // The API section lived in the sidebar until 0.4.2: old links land on its new place.
  const apiDocsOld = createRoute({ getParentRoute: () => app, path: "/api-docs", beforeLoad: () => { throw redirect({ to: "/settings/api" }); } });

  const routeTree = root.addChildren([login, supportEnter, app.addChildren([dashboard, users, tariffs, inbounds, nodes, diagnostics, promocodes, payments, addons, telegram, torrent, filters, market, addonApp, telegramOld, apiDocs, apiDocsOld, webhooks, settings])]);
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
