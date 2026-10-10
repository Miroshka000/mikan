import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { UsersSearch } from "../admin/search";
import { api, rawApi, unwrap, type Schemas, type User } from "./client";

export const qk = {
  me: ["me"] as const,
  users: ["users"] as const,
  user: (id: number) => ["users", "one", id] as const,
  userTraffic: (id: number) => ["users", "traffic", id] as const,
  devices: (id: number) => ["users", "devices", id] as const,
  boundDevices: (id: number) => ["users", "bound", id] as const,
  deviceBans: (id: number) => ["users", "bans", id] as const,
  tariffs: ["tariffs"] as const,
  inbounds: ["inbounds"] as const,
  presets: ["presets"] as const,
  overview: ["overview"] as const,
  traffic: (range: string) => ["traffic", range] as const,
  node: ["node"] as const,
  nodes: ["nodes"] as const,
  settings: ["settings"] as const,
  sessions: ["sessions"] as const,
  telegram: ["telegram"] as const,
  updates: ["updates"] as const,
  apiKeys: ["api-keys"] as const,
  payments: ["payments"] as const,
  paymentSettings: ["payment-settings"] as const,
  addons: ["addons"] as const,
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
  torrentHits: (user: number) => ["torrent", "hits", user] as const,
  speedTests: (node: number) => ["speedtests", node] as const,
  subPage: ["sub-page"] as const,
  subDocs: ["sub-docs"] as const,
  folders: ["folders"] as const,
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

/** The users page lists everyone it can (the API's cap); a card that shows a few asks for just those. */
const USERS_MAX = 500;

export function useUsers(f: UsersFilter, o: { limit?: number; refetchInterval?: number } = {}) {
  const limit = o.limit ?? USERS_MAX;
  return useQuery({
    queryKey: [...qk.users, "list", f, limit],
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/v1/users", {
          params: { query: { state: f.state, q: f.q || undefined, folder: f.folder === undefined ? undefined : String(f.folder), source: f.source, hidden: f.hidden, limit } },
          signal,
        }),
      ),
    placeholderData: keepPreviousData,
    // Who is online is the only thing in the list that moves on its own; edits refresh it at once.
    refetchInterval: o.refetchInterval ?? 30_000,
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

export function useTorrent() {
  return useQuery({ queryKey: qk.torrent, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/torrent", { signal })) });
}

/** The ingress and egress filters of the nodes. */
export function useFilters() {
  return useQuery({ queryKey: qk.filters, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/filters", { signal })) });
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

/** Mutations that change a user refresh every user-related view. */
export function useUserMutation<TArgs, TResult>(fn: (args: TArgs) => Promise<TResult>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: () => {
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


export type PromoCode = {
  id: number;
  code: string;
  name: string;
  description: string;
  type: "days" | "traffic" | "percent" | "fixed";
  value: number;
  currency: string;
  starts_at?: string;
  ends_at?: string;
  max_uses?: number;
  used_count: number;
  per_user_limit: number;
  discount_ttl: number;
  min_order: number;
  max_discount: number;
  tariff_ids: number[];
  pool_id?: number;
  first_purchase_only: boolean;
  new_users_only: boolean;
  enabled: boolean;
  status: string;
  created_at: string;
  created_by?: number;
};

export type PromoRedemption = {
  id: number;
  promo_id: number;
  user_id?: number;
  tg_id: number;
  payment_id?: number;
  status: string;
  redeemed_at: string;
  expires_at?: string;
  days: number;
  bytes: number;
  discount_amount: number;
  original_amount: number;
  final_amount: number;
  currency: string;
  code: string;
};

type PromoBody = {
  code: string; name: string; description: string; type: PromoCode["type"]; value: number; currency: string;
  starts_at?: number; ends_at?: number; max_uses?: number; per_user_limit: number; discount_ttl: number;
  min_order: number; max_discount: number; tariff_ids: number[]; pool_id?: number; first_purchase_only: boolean; new_users_only: boolean; enabled: boolean;
};

type PromoList = { items: PromoCode[]; total: number };
type PromoRedemptionList = { items: PromoRedemption[]; total: number };

export function usePromocodes() {
  return useQuery({
    queryKey: qk.promocodes,
    queryFn: ({ signal }) => rawJson<PromoList>("/api/v1/promocodes?limit=100", { signal }),
  });
}

export function usePromoRedemptions() {
  return useQuery({
    queryKey: qk.promocodeRedemptions,
    queryFn: ({ signal }) => rawJson<PromoRedemptionList>("/api/v1/promocodes/redemptions?limit=100", { signal }),
  });
}

export function usePromoMutations() {
  const queryClient = useQueryClient();
  const refresh = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: qk.promocodes }),
    queryClient.invalidateQueries({ queryKey: qk.promocodeRedemptions }),
  ]);
  const create = useMutation({
    mutationFn: (body: PromoBody) => rawJson<PromoCode>("/api/v1/promocodes", { method: "POST", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: refresh,
  });
  const update = useMutation({
    mutationFn: ({ id, body }: { id: number; body: PromoBody }) => rawJson<PromoCode>(`/api/v1/promocodes/${id}`, { method: "PUT", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: refresh,
  });
  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => rawJson<PromoCode>(`/api/v1/promocodes/${id}/enabled`, { method: "POST", body: JSON.stringify({ enabled }), headers: { "Content-Type": "application/json" } }),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (id: number) => rawJson<Record<string, never>>(`/api/v1/promocodes/${id}`, { method: "DELETE" }),
    onSuccess: refresh,
  });
  return { create, update, toggle, remove };
}

async function rawJson<T>(path: string, init: RequestInit = {}): Promise<T> {
  return rawApi(path, init) as Promise<T>;
}
