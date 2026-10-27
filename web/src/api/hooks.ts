import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { UsersSearch } from "../admin/search";
import { nextOffset } from "../lib/paging";
import { api, unwrap, type Schemas, type User } from "./client";

export const qk = {
  me: ["me"] as const,
  users: ["users"] as const,
  user: (id: number) => ["users", "one", id] as const,
  userTraffic: (id: number) => ["users", "traffic", id] as const,
  devices: (id: number) => ["users", "devices", id] as const,
  boundDevices: (id: number) => ["users", "bound", id] as const,
  deviceBans: (id: number) => ["users", "bans", id] as const,
  userOnline: (id: number) => ["users", "online", id] as const,
  tariffs: ["tariffs"] as const,
  inbounds: ["inbounds"] as const,
  presets: ["presets"] as const,
  overview: ["overview"] as const,
  traffic: (range: string) => ["traffic", range] as const,
  node: ["node"] as const,
  nodes: ["nodes"] as const,
  settings: ["settings"] as const,
  sessions: ["sessions"] as const,
  passkeys: ["passkeys"] as const,
  telegram: ["telegram"] as const,
  updates: ["updates"] as const,
  apiKeys: ["api-keys"] as const,
  payments: ["payments"] as const,
  paymentSettings: ["payment-settings"] as const,
  addons: ["addons"] as const,
  marketplace: ["marketplace"] as const,
  addonSources: ["addon-sources"] as const,
  botSlot: ["bot-slot"] as const,
  addonStorage: ["addon-storage"] as const,
  addonPage: (id: string) => ["addon-page", id] as const,
  telegramBackup: ["telegram-backup"] as const,
  warp: (node: number) => ["warp", node] as const,
  cascade: (node: number) => ["cascade", node] as const,
  pools: ["pools"] as const,
  userPools: (id: number) => ["users", "pools", id] as const,
  packages: ["packages"] as const,
  promocodes: ["promocodes"] as const,
  promocodeRedemptions: ["promocodes", "redemptions"] as const,
  userGrants: (id: number) => ["users", "grants", id] as const,
  torrent: ["torrent"] as const,
  filters: ["filters"] as const,
  trafficDetail: ["traffic-detail"] as const,
  exitDns: ["exit-dns"] as const,
  torrentHits: (user: number) => ["torrent", "hits", user] as const,
  speedTests: (node: number) => ["speedtests", node] as const,
  subPage: ["sub-page"] as const,
  subDocs: ["sub-docs"] as const,
  subApps: ["sub-apps"] as const,
  subRules: ["sub-rules"] as const,
  subTemplates: ["sub-templates"] as const,
  subTemplate: (id: number) => ["sub-templates", "one", id] as const,
  readySubTemplates: ["sub-templates-ready"] as const,
  userSubLog: (id: number) => ["users", "sub-log", id] as const,
  folders: ["folders"] as const,
  lifecycle: ["lifecycle"] as const,
  // Under the users: a change of any of them may move who the removal reaches.
  inactive: (days: number) => ["users", "inactive", days] as const,
  nodeTraffic: (node: number, range: string) => ["node-traffic", node, range] as const,
  nodeShares: (range: string) => ["node-shares", range] as const,
};

export const meQuery = {
  queryKey: qk.me,
  queryFn: ({ signal }: { signal: AbortSignal }) => unwrap(api.GET("/api/v1/auth/me", { signal })),
  staleTime: 60_000,
  retry: false,
};

/** What the list is asked for: left out, a part of the filter is "any" (hidden: everyone, as
 * the API has it; the Users page asks for "hide"). */
type UsersFilter = Pick<UsersSearch, "q" | "folder" | "source"> & {
  /** "attention": out of traffic, expiring and expired in that order, for the overview. */
  state: UsersSearch["state"] | "attention";
  hidden?: "hide" | "show" | "only";
};

const usersQuery = (f: UsersFilter) => ({ state: f.state, q: f.q || undefined, folder: f.folder === undefined ? undefined : String(f.folder), source: f.source, hidden: f.hidden });

/** A card that shows a few users asks for just those. */
export function useUsers(f: UsersFilter, o: { limit: number; refetchInterval?: number }) {
  return useQuery({
    queryKey: [...qk.users, "list", f, o.limit],
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users", { params: { query: { ...usersQuery(f), limit: o.limit } }, signal })),
    placeholderData: keepPreviousData,
    // Who is online is the only thing in the list that moves on its own; edits refresh it at once.
    refetchInterval: o.refetchInterval ?? 30_000,
  });
}

// The users page asks for as many as the API gives at once, then the next ones ("show more").
const USERS_PAGE = 500;

/** The users page's list, a page at a time. The poll asks every page loaded again, so it
 * slows down with them. */
export function useUsersPages(f: UsersFilter) {
  return useInfiniteQuery({
    queryKey: [...qk.users, "pages", f],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) => unwrap(api.GET("/api/v1/users", { params: { query: { ...usersQuery(f), limit: USERS_PAGE, offset: pageParam } }, signal })),
    getNextPageParam: nextOffset,
    placeholderData: keepPreviousData,
    refetchInterval: (q) => 30_000 * Math.max(1, q.state.data?.pages.length ?? 1),
  });
}

export function useUser(id: number | undefined) {
  return useQuery({
    queryKey: qk.user(id ?? 0),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}", { params: { path: { id: id! } }, signal })),
    enabled: !!id,
    refetchInterval: 15_000,
  });
}

export function useUserTraffic(id: number) {
  return useQuery({
    queryKey: qk.userTraffic(id),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/traffic", { params: { path: { id }, query: { range: "30d" } }, signal })),
  });
}

export function useDevices(id: number) {
  return useQuery({
    queryKey: qk.devices(id),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/devices", { params: { path: { id } }, signal })),
    refetchInterval: 30_000,
  });
}

/** Devices bound to the subscription (each with keys of its own). */
export function useBoundDevices(id: number) {
  return useQuery({
    queryKey: qk.boundDevices(id),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/bound-devices", { params: { path: { id } }, signal })),
    refetchInterval: 30_000,
  });
}

/** Devices banned from a user's subscription; they change only from the card. */
export function useDeviceBans(id: number) {
  return useQuery({
    queryKey: qk.deviceBans(id),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/device-bans", { params: { path: { id } }, signal })),
  });
}

/** Where a user is online now, by node and by device: the nodes report every few seconds. */
export function useUserOnline(id: number) {
  return useQuery({
    queryKey: qk.userOnline(id),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/online", { params: { path: { id } }, signal })),
    refetchInterval: 15_000,
  });
}

export function useTariffs() {
  return useQuery({ queryKey: qk.tariffs, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/tariffs", { signal })) });
}

export function useInbounds() {
  // The listeners' state; an edit refreshes the list at once.
  return useQuery({ queryKey: qk.inbounds, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/inbounds", { signal })), refetchInterval: 30_000 });
}

export function usePresets() {
  return useQuery({ queryKey: qk.presets, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/presets", { signal })), staleTime: Infinity });
}

/**
 * The overview's figures. The overview page polls them; elsewhere (the menu's count of
 * clients) they are read once and refreshed by edits and on return to the tab: each read
 * counts every user and sums the month's traffic.
 */
export function useOverview(o: { poll?: boolean } = {}) {
  return useQuery({
    queryKey: qk.overview,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/stats/overview", { signal })),
    refetchInterval: o.poll === false ? false : 15_000,
  });
}

export function useServerTraffic(range: "24h" | "7d" | "30d") {
  return useQuery({
    queryKey: qk.traffic(range),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/stats/traffic", { params: { query: { range } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
  });
}

export function useNode() {
  return useQuery({ queryKey: qk.node, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/node", { signal })), refetchInterval: 10_000 });
}

/** The nodes; `watch` polls closely while something waits for a node (a new one joining). */
export function useNodes(watch?: boolean) {
  return useQuery({
    queryKey: qk.nodes,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/nodes", { signal })),
    // Closer while a node updates: it goes down and comes back on the new version.
    refetchInterval: (q) => (watch ? 2_500 : q.state.data?.some((n) => n.update?.state === "running") ? 4_000 : 30_000),
  });
}

/** Payment settings: also whether selling is on, which shows Payments in the menu. */
export function usePaymentSettings() {
  return useQuery({ queryKey: qk.paymentSettings, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/payments/settings", { signal })) });
}

/** The payment methods (protocol 1 adapters): followed every few seconds while the server works on a request. */
export function useAddons() {
  return useQuery({
    queryKey: qk.addons,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/addons", { signal })),
    refetchInterval: (q) => (q.state.data?.queue?.length ? 3_000 : false),
  });
}

/** The marketplace of protocol 2: followed every few seconds while the server works on a request. */
export function useMarketplace() {
  return useQuery({
    queryKey: qk.marketplace,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/marketplace", { signal })),
    refetchInterval: (q) => (q.state.data?.queue?.length ? 3_000 : false),
  });
}

/** The sources of third-party addons. The server reads them from their own servers, so it is asked only while the drawer is open. */
export function useAddonSources(enabled: boolean) {
  return useQuery({
    queryKey: qk.addonSources,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/addon-sources", { signal })),
    enabled,
    staleTime: 0,
  });
}

/** Which addon answers in the subscribers' bot; empty while none does. */
export function useBotSlot() {
  return useQuery({
    queryKey: qk.botSlot,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/telegram/bot-addon", { signal })),
  });
}

export function useTorrent() {
  return useQuery({ queryKey: qk.torrent, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/torrent", { signal })) });
}

/** The ingress and egress filters of the nodes. */
export function useFilters() {
  return useQuery({ queryKey: qk.filters, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/filters", { signal })) });
}

/** Whether the nodes count the subscriptions' traffic by site, and for how many days. */
export function useTrafficDetail() {
  return useQuery({ queryKey: qk.trafficDetail, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/traffic-detail", { signal })) });
}

/** The DNS at the exit of the nodes. */
export function useExitDns() {
  return useQuery({ queryKey: qk.exitDns, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/exit-dns", { signal })) });
}

/** The torrent blocker's catches, newest first, a page at a time; user 0: everyone's. */
export function useTorrentHits(user = 0, limit = 50) {
  return useInfiniteQuery({
    queryKey: [...qk.torrentHits(user), limit],
    queryFn: ({ pageParam, signal }) =>
      unwrap(api.GET("/api/v1/torrent/hits", { params: { query: { user_id: user || undefined, before: pageParam || undefined, limit } }, signal })),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length < limit ? undefined : last[last.length - 1]!.id),
    refetchInterval: 30_000,
  });
}

export function useSettings() {
  return useQuery({ queryKey: qk.settings, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/settings", { signal })) });
}

/** The rules of the subscriptions' lifecycle: the carry-over of traffic and the removal of the inactive. */
export function useLifecycle() {
  return useQuery({ queryKey: qk.lifecycle, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/lifecycle", { signal })) });
}

/** Whom the removal of the inactive reaches with days (the saved number, or one being tried). */
export function useInactive(days: number, enabled = true) {
  return useQuery({
    queryKey: qk.inactive(days),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/lifecycle/inactive", { params: { query: { days } }, signal })),
    placeholderData: keepPreviousData,
    enabled,
  });
}

/** Followed every few seconds while the server updates, hourly otherwise. */
export function useUpdates() {
  return useQuery({
    queryKey: qk.updates,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/updates", { signal })),
    refetchInterval: (q) => (q.state.data?.requested_at || q.state.data?.host?.state === "running" ? 5_000 : 3_600_000),
    retry: (n) => n < 30,
    retryDelay: 3_000,
  });
}

/** What a user mutation can change: "devices" touches only the user's devices (renaming,
 * unbinding, banning), so the traffic, the dashboard and the folders are not asked again. */
type UserScope = "all" | "devices";

/** The user views a devices mutation changes: the user (its device count), the list (the
 * same count), the device lists and where the user is online. */
const DEVICE_VIEWS = new Set(["one", "list", "pages", "devices", "bound", "bans", "online"]);

/** Mutations that change a user refresh the user-related views they change. */
export function useUserMutation<TArgs, TResult>(fn: (args: TArgs) => Promise<TResult>, scope: UserScope = "all") {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: () => {
      if (scope === "devices") {
        void qc.invalidateQueries({ queryKey: qk.users, predicate: (q) => DEVICE_VIEWS.has(String(q.queryKey[1])) });
        return;
      }
      void qc.invalidateQueries({ queryKey: qk.users });
      void qc.invalidateQueries({ queryKey: qk.overview });
      // A user moved in or out of a folder changes its count.
      void qc.invalidateQueries({ queryKey: qk.folders });
    },
  });
}

export const userActions = {
  create: (body: Schemas["CreateUserInputBody"]) => unwrap(api.POST("/api/v1/users", { body })),
  update: ({ id, body }: { id: number; body: Schemas["PatchUserInputBody"] }) =>
    unwrap(api.PATCH("/api/v1/users/{id}", { params: { path: { id } }, body })),
  extend: ({ id, ...body }: { id: number } & Schemas["ExtendInputBody"]) => unwrap(api.POST("/api/v1/users/{id}/extend", { params: { path: { id } }, body })),
  reset: (id: number) => unwrap(api.POST("/api/v1/users/{id}/reset-traffic", { params: { path: { id } } })),
  reissue: (id: number) => unwrap(api.POST("/api/v1/users/{id}/reissue", { params: { path: { id } } })),
  remove: (id: number) => unwrap(api.DELETE("/api/v1/users/{id}", { params: { path: { id } } })),
  bulk: (body: Schemas["BulkInputBody"]) => unwrap(api.POST("/api/v1/users/bulk", { body })),
  bulkPreview: (body: Schemas["BulkInputBody"]) => unwrap(api.POST("/api/v1/users/bulk/preview", { body })),
  setNextTariff: ({ id, ...body }: { id: number } & Schemas["NextTariffInputBody"]) =>
    unwrap(api.PUT("/api/v1/users/{id}/next-tariff", { params: { path: { id } }, body })),
  clearNextTariff: (id: number) => unwrap(api.DELETE("/api/v1/users/{id}/next-tariff", { params: { path: { id } } })),
  unbindDevice: ({ id, device }: { id: number; device: number }) =>
    unwrap(api.DELETE("/api/v1/users/{id}/bound-devices/{device}", { params: { path: { id, device } } })),
  renameDevice: ({ id, device, name }: { id: number; device: number; name: string }) =>
    unwrap(api.PATCH("/api/v1/users/{id}/bound-devices/{device}", { params: { path: { id, device } }, body: { name } })),
  banDevice: ({ id, device }: { id: number; device: number }) => unwrap(api.POST("/api/v1/users/{id}/bound-devices/{device}/ban", { params: { path: { id, device } } })),
  unbanDevice: ({ id, ban }: { id: number; ban: number }) => unwrap(api.DELETE("/api/v1/users/{id}/device-bans/{ban}", { params: { path: { id, ban } } })),
};

/** One paid period: a month up to the billing day, or 30 days without one. */
export const onePeriod = (u: Pick<User, "billing_day">): Schemas["ExtendInputBody"] => (u.billing_day != null ? { months: 1 } : { days: 30 });

export function useFolders() {
  return useQuery({ queryKey: qk.folders, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/folders", { signal })) });
}

/** A folder changes what the list shows and counts: the users' views are refreshed with the folders. */
export function useFolderMutation<TArgs, TResult>(fn: (args: TArgs) => Promise<TResult>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.folders });
      void qc.invalidateQueries({ queryKey: qk.users });
    },
  });
}

export const folderActions = {
  create: (body: Schemas["CreateFolderInputBody"]) => unwrap(api.POST("/api/v1/folders", { body })),
  update: ({ id, body }: { id: number; body: Schemas["PatchFolderInputBody"] }) => unwrap(api.PATCH("/api/v1/folders/{id}", { params: { path: { id } }, body })),
  remove: (id: number) => unwrap(api.DELETE("/api/v1/folders/{id}", { params: { path: { id } } })),
  order: (ids: number[]) => unwrap(api.PUT("/api/v1/folders/order", { body: { ids } })),
};

/** One node's traffic by the hour (24 h, 7 days) or by the day (30 days). */
export function useNodeTraffic(id: number | undefined, range: "24h" | "7d" | "30d") {
  return useQuery({
    queryKey: qk.nodeTraffic(id ?? 0, range),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/nodes/{id}/traffic", { params: { path: { id: id! }, query: { range } }, signal })),
    enabled: !!id,
    // Another range of the same node keeps its chart while loading; another node never
    // shows the previous one's.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === (id ?? 0) ? prev : undefined),
    refetchInterval: 60_000,
  });
}

/** What every node carried in a range, biggest first: the dashboard's share of each. */
export function useNodeShares(range: "24h" | "7d" | "30d", enabled = true) {
  return useQuery({
    queryKey: qk.nodeShares(range),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/stats/nodes", { params: { query: { range } }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
    enabled,
  });
}

export function usePools() {
  return useQuery({ queryKey: qk.pools, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/pools", { signal })) });
}

export function usePackages() {
  return useQuery({ queryKey: qk.packages, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/packages", { signal })) });
}

export function useUserGrants(id: number) {
  return useQuery({ queryKey: qk.userGrants(id), queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/grants", { params: { path: { id } }, signal })) });
}


/** Promo codes and their history are what the API's schema says they are. */
export type PromoCode = Schemas["PromoCodeView"];
export type PromoRedemption = Schemas["PromoRedemptionView"];
type PromoBody = Schemas["PromoBody"];

// The promo lists come a page at a time, the next one by its offset ("show more").
const PROMO_PAGE = 100;

export function usePromocodes() {
  return useInfiniteQuery({
    queryKey: [...qk.promocodes, "list"],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) => unwrap(api.GET("/api/v1/promocodes", { params: { query: { limit: PROMO_PAGE, offset: pageParam } }, signal })),
    getNextPageParam: nextOffset,
  });
}

/** The activations, newest first; q is searched on the server (a part of the code, a client's id). */
export function usePromoRedemptions(q: string) {
  return useInfiniteQuery({
    queryKey: [...qk.promocodeRedemptions, q],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      unwrap(api.GET("/api/v1/promocodes/redemptions", { params: { query: { limit: PROMO_PAGE, offset: pageParam, q: q || undefined } }, signal })),
    getNextPageParam: nextOffset,
    placeholderData: keepPreviousData,
  });
}

export function usePromoMutations() {
  const queryClient = useQueryClient();
  const refresh = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: qk.promocodes }),
    queryClient.invalidateQueries({ queryKey: qk.promocodeRedemptions }),
  ]);
  const create = useMutation({
    mutationFn: (body: PromoBody) => unwrap(api.POST("/api/v1/promocodes", { body })),
    onSuccess: refresh,
  });
  const update = useMutation({
    mutationFn: ({ id, body }: { id: number; body: PromoBody }) => unwrap(api.PUT("/api/v1/promocodes/{id}", { params: { path: { id } }, body })),
    onSuccess: refresh,
  });
  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => unwrap(api.POST("/api/v1/promocodes/{id}/enabled", { params: { path: { id } }, body: { enabled } })),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/promocodes/{id}", { params: { path: { id } } })),
    onSuccess: refresh,
  });
  return { create, update, toggle, remove };
}

/**
 * A pool changed or went: everything that shows pools is asked again - the tariffs' limits
 * in it, the protocols that belong to it and the packages bought into it.
 */
export function invalidatePoolRelated(qc: QueryClient) {
  for (const key of [qk.pools, qk.tariffs, qk.inbounds, qk.packages]) void qc.invalidateQueries({ queryKey: key });
}

/** The bot's settings and state. A broadcast in progress moves every second; otherwise little changes. */
export function useTelegram() {
  return useQuery({
    queryKey: qk.telegram,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/telegram", { signal })),
    refetchInterval: (q) => (q.state.data?.broadcast?.active ? 2_000 : 10_000),
  });
}

/** How many speed tests of a node are kept and shown; the list and the cache agree on it. */
export const SPEEDTESTS_LIMIT = 30;

/** A node's speed tests, newest first; nothing is asked until there is a node. */
export function useSpeedTests(node: number | undefined) {
  return useQuery({
    queryKey: qk.speedTests(node ?? 0),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/nodes/{id}/speedtests", { params: { path: { id: node! }, query: { limit: SPEEDTESTS_LIMIT } }, signal })),
    enabled: !!node,
  });
}

/** The known apps and which of them the admin turned away. */
export function useSubApps() {
  return useQuery({ queryKey: qk.subApps, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-apps", { signal })) });
}

/** The response rules of the subscription, and what the editor needs to keep within their limits. */
export function useSubRules() {
  return useQuery({ queryKey: qk.subRules, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-rules", { signal })) });
}

/** The subscription templates of every core, without their texts, and how many a core may have. */
export function useSubTemplates() {
  return useQuery({ queryKey: qk.subTemplates, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-templates", { signal })) });
}

/** One subscription template with its text; its text is hidden from a key or an operator not shown secrets. */
export function useSubTemplate(id: number | undefined) {
  return useQuery({
    queryKey: qk.subTemplate(id ?? 0),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-templates/{id}", { params: { path: { id: id! } }, signal })),
    enabled: id !== undefined,
  });
}

/** The catalog of ready subscription templates: mikan's own and the community's, without their texts. */
export function useReadySubTemplates() {
  return useQuery({ queryKey: qk.readySubTemplates, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-templates/ready", { signal })), staleTime: Infinity });
}

/** The latest requests of a user's subscription, newest first. */
export function useUserSubLog(id: number) {
  return useQuery({ queryKey: qk.userSubLog(id), queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/sub-log", { params: { path: { id }, query: { limit: 50 } }, signal })) });
}
