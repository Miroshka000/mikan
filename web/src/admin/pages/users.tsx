import * as Menu from "@radix-ui/react-dropdown-menu";
import { useNavigate, useSearch } from "@tanstack/react-router";
import clsx from "clsx";
import { CalendarPlus, Check, ChevronRight, Eye, EyeOff, FolderCog, FolderInput, Plus, Power, RotateCcw, Search, SlidersHorizontal, Trash2, X } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorText, type Tariff, type User } from "../../api/client";
import { userActions, useFolders, useOverview, useSettings, useTariffs, useUserMutation, useUsers } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Avatar, Bar, Button, EmptyState, PageHeader, Skeleton, StatePill } from "../../components/ui";
import { t, useLocale } from "../../i18n";
import { bytes, dateShort, expiryText, num } from "../../lib/format";
import { useMediaQuery } from "../../lib/media";
import { USER_HIDDEN, USER_SOURCES, USER_STATES, type UsersSearch } from "../search";
import { CreateUserDrawer } from "./user-create";
import { UserDrawer } from "./user-drawer";
import { FolderBadge, FolderMark, FolderMenuItems, FoldersDrawer, type Folder } from "./user-folders";

/** A row of chips: one line to scroll sideways on a phone, wrapping on a wide screen. */
const CHIP_ROW = "-mx-3 flex items-center gap-2 overflow-x-auto px-3 pb-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden lg:mx-0 lg:flex-wrap lg:overflow-visible lg:px-0 lg:pb-0";

/** One choice of the filters menu: a tick when chosen, how many users it holds. */
function FilterItem({ value, label, count, mark }: { value: string; label: string; count?: number; mark?: React.ReactNode }) {
  return (
    <Menu.RadioItem value={value} className="menu-item">
      <span className="grid w-4 shrink-0 place-items-center">
        <Menu.ItemIndicator>
          <Check size={16} aria-hidden />
        </Menu.ItemIndicator>
      </span>
      {mark}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {count !== undefined ? <span className="num text-xs text-[var(--ink-500)]">{num(count)}</span> : null}
    </Menu.RadioItem>
  );
}

/** A filter from the menu that is on, with the cross that takes it off. */
function ActiveFilter({ label, onClear }: { label: string; onClear: () => void }) {
  return (
    <span className="chip h-8 shrink-0 gap-1 pr-1 text-[var(--ink-900)]">
      <span className="max-w-[240px] truncate">{label}</span>
      <button type="button" className="icon-btn h-6 w-6" aria-label={t("users.clearFilter", { name: label })} onClick={onClear}>
        <X size={16} aria-hidden />
      </button>
    </span>
  );
}

/** What an empty list says: the filter that emptied it, the most specific first. */
function emptyText(s: UsersSearch, total: number, hidden: number): { title: string; text: string } {
  if (s.q) return { title: t("users.notFoundTitle"), text: t("users.notFoundQuery", { q: s.q }) };
  if (s.hidden === "only") return { title: t("users.hiddenEmptyTitle"), text: t("users.hiddenEmptyText") };
  if (typeof s.folder === "number") return { title: t("users.folderEmptyTitle"), text: t("users.folderEmptyText") };
  if (s.folder === "none") return { title: t("users.noFolderEmptyTitle"), text: t("users.noFolderEmptyText") };
  if (s.source) return { title: t("users.sourceEmptyTitle"), text: t("users.sourceEmptyText") };
  if (s.hidden === undefined && hidden === total) return { title: t("users.allHiddenTitle"), text: t("users.allHiddenText") };
  return { title: t("users.notFoundTitle"), text: t("users.notFoundGroup") };
}

/** Where a user comes from, folder and hidden mark: the small print under the name. */
function Labels({ u, folder }: { u: User; folder?: Folder }) {
  return (
    <>
      {u.source !== "admin" ? (
        <span className="tag" title={t(`users.sourceHints.${u.source}`)}>
          {t(`users.sources.${u.source}`)}
        </span>
      ) : null}
      {folder ? <FolderBadge folder={folder} /> : null}
      {u.hidden ? (
        <span className="tag gap-1" title={t("users.hiddenBadge")}>
          <EyeOff size={12} aria-hidden />
          {t("users.hiddenShort")}
        </span>
      ) : null}
    </>
  );
}

export function UsersPage() {
  const search = useSearch({ from: "/_app/users" });
  const navigate = useNavigate({ from: "/users" });
  const [q, setQ] = useState(search.q);
  // The last search text this page itself put into the URL: a different one in the URL
  // came from outside (Back, a link, the reset button) and replaces what is typed.
  const written = useRef(search.q);
  // The list hides the hidden unless asked: the API's own default shows everyone.
  const users = useUsers({ state: search.state, q: search.q, folder: search.folder, source: search.source, hidden: search.hidden ?? "hide" });
  const tariffs = useTariffs();
  const folders = useFolders();
  const overview = useOverview({ poll: false });
  const [managing, setManaging] = useState(false);
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set());
  const narrow = useMediaQuery("(max-width: 767px)");
  // With binding a place is a bound device, else an address online (see the drawer). Read
  // once here: a row of its own would be one more subscriber to the settings per user.
  const binding = !!useSettings().data?.device_binding;

  // A folder that is gone (deleted here, or elsewhere) is no filter any more: a link to it
  // would show an empty list that nothing explains.
  useEffect(() => {
    const list = folders.data;
    if (typeof search.folder === "number" && list && !list.some((f) => f.id === search.folder)) {
      void navigate({ search: (s) => ({ ...s, folder: undefined }), replace: true });
    }
  }, [folders.data, search.folder, navigate]);

  useEffect(() => {
    if (search.q === written.current) return;
    written.current = search.q;
    setQ(search.q);
  }, [search.q]);

  useEffect(() => {
    if (q === written.current) return;
    const timer = window.setTimeout(() => {
      written.current = q;
      void navigate({ search: (s) => ({ ...s, q }), replace: true });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [q, navigate]);

  const tariffById = useMemo(() => new Map((tariffs.data ?? []).map((tr) => [tr.id, tr])), [tariffs.data]);
  const folderList = useMemo(() => folders.data ?? [], [folders.data]);
  const folderById = useMemo(() => new Map(folderList.map((f) => [f.id, f])), [folderList]);
  const counts = users.data?.counts;
  const items = useMemo(() => users.data?.items ?? [], [users.data]);
  // What a bulk action touches is what the admin sees ticked: a row that left the list (a
  // search, another filter, a user deleted elsewhere) drops out of the selection with it.
  const chosen = useMemo(() => items.filter((u) => selected.has(u.id)), [items, selected]);
  const allSelected = items.length > 0 && chosen.length === items.length;
  const openUser = useCallback((id?: number) => void navigate({ search: (s) => ({ ...s, user: id, create: undefined }) }), [navigate]);
  const toggle = useCallback(
    (id: number) =>
      setSelected((s) => {
        const n = new Set(s);
        if (n.has(id)) n.delete(id);
        else n.add(id);
        return n;
      }),
    [],
  );
  const clear = useCallback(() => setSelected(new Set()), []);
  // A filter changes what the list holds: what was ticked may be gone from it.
  const setFilter = (patch: Partial<UsersSearch>) => {
    clear();
    void navigate({ search: (s) => ({ ...s, ...patch }) });
  };
  const resetFilters = () => {
    written.current = "";
    setQ("");
    clear();
    void navigate({ search: { state: "all", q: "" } });
  };
  const hiddenTotal = users.data?.hidden_total ?? 0;
  const sourceCounts = users.data?.source_counts;
  const folderCounts = users.data?.folder_counts;
  const sum = (m?: Record<string, number>) => Object.values(m ?? {}).reduce((a, b) => a + b, 0);
  // Folders and sources are rows of their own only once there is something to tell apart:
  // a panel that never made a folder or took a user from elsewhere sees neither.
  const showFolders = folderList.length > 0 || search.folder !== undefined;
  const showSources = (sourceCounts && sum(sourceCounts) - sourceCounts.admin! > 0) || search.source !== undefined;
  const showHidden = hiddenTotal > 0 || search.hidden !== undefined;
  // The filters of the menu that are on, beside the state chips and the search.
  const extra = (search.folder !== undefined ? 1 : 0) + (search.source ? 1 : 0) + (search.hidden ? 1 : 0);

  return (
    <>
      <PageHeader
        title={t("nav.users")}
        sub={overview.data ? `${t("users.subtitle", { n: overview.data.users_total, active: num(overview.data.users_active) })}${hiddenTotal > 0 ? ` · ${t("users.hiddenCount", { n: hiddenTotal })}` : ""}` : "…"}
        actions={
          <>
            <Button onClick={() => setManaging(true)} aria-label={t("users.manageFolders")}>
              <FolderCog size={18} aria-hidden />
              <span className="max-[760px]:hidden">{t("users.manageFolders")}</span>
            </Button>
            <Button variant="primary" onClick={() => void navigate({ search: (s) => ({ ...s, create: true, user: undefined }) })}>
              <Plus size={18} aria-hidden />
              <span className="max-[760px]:hidden">{t("dashboard.newUser")}</span>
            </Button>
          </>
        }
      />
      <div className="reveal flex flex-col gap-3">
        <div className="flex flex-col items-stretch justify-between gap-3 lg:flex-row lg:items-center">
          <div className={CHIP_ROW} role="group" aria-label={t("users.filter")}>
            {USER_STATES.map((f) => (
              <button key={f} type="button" className="chip shrink-0" aria-pressed={search.state === f} onClick={() => setFilter({ state: f })}>
                {t(`users.filters.${f}`)}
                {counts ? <span className="chip-count num">{num(counts[f])}</span> : null}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-2">
            <label className="search-field">
              <Search size={16} aria-hidden />
              <input type="search" placeholder={t("users.searchPlaceholder")} value={q} onChange={(e) => setQ(e.target.value)} aria-label={t("users.searchLabel")} />
            </label>
            {showFolders || showSources || showHidden ? (
              <Menu.Root>
                <Menu.Trigger asChild>
                  <button type="button" className="chip h-10 shrink-0" aria-pressed={extra > 0}>
                    <SlidersHorizontal size={16} aria-hidden />
                    <span className="max-[480px]:sr-only">{t("users.moreFilters")}</span>
                    {extra ? <span className="chip-count num">{extra}</span> : null}
                  </button>
                </Menu.Trigger>
                <Menu.Portal>
                  <Menu.Content className="menu glass-strong max-h-[min(480px,70vh)] w-[280px] overflow-y-auto" align="end" sideOffset={8}>
                    {showFolders ? (
                      <>
                        <Menu.Label className="menu-label">{t("users.foldersLabel")}</Menu.Label>
                        <Menu.RadioGroup value={search.folder === undefined ? "all" : String(search.folder)} onValueChange={(v) => setFilter({ folder: v === "all" ? undefined : v === "none" ? "none" : Number(v) })} aria-label={t("users.folderFilter")}>
                          <FilterItem value="all" label={t("users.foldersAll")} count={folderCounts ? sum(folderCounts) : undefined} />
                          <FilterItem value="none" label={t("users.foldersNone")} count={folderCounts ? (folderCounts.none ?? 0) : undefined} />
                          {folderList.map((f) => (
                            <FilterItem key={f.id} value={String(f.id)} label={f.name} mark={<FolderMark folder={f} />} count={folderCounts ? (folderCounts[f.id] ?? 0) : undefined} />
                          ))}
                        </Menu.RadioGroup>
                      </>
                    ) : null}
                    {showSources ? (
                      <>
                        {showFolders ? <Menu.Separator className="menu-sep" /> : null}
                        <Menu.Label className="menu-label">{t("users.sourceLabel")}</Menu.Label>
                        <Menu.RadioGroup value={search.source ?? "all"} onValueChange={(v) => setFilter({ source: v === "all" ? undefined : (v as UsersSearch["source"]) })} aria-label={t("users.sourceFilter")}>
                          {USER_SOURCES.map((s) => (
                            <FilterItem key={s} value={s} label={t(`users.sources.${s}`)} count={sourceCounts ? (s === "all" ? sum(sourceCounts) : (sourceCounts[s] ?? 0)) : undefined} />
                          ))}
                        </Menu.RadioGroup>
                      </>
                    ) : null}
                    {showHidden ? (
                      <>
                        {showFolders || showSources ? <Menu.Separator className="menu-sep" /> : null}
                        <Menu.Label className="menu-label">{t("users.hiddenChip", { n: hiddenTotal })}</Menu.Label>
                        <Menu.RadioGroup value={search.hidden ?? "hide"} onValueChange={(v) => setFilter({ hidden: v === "hide" ? undefined : (v as UsersSearch["hidden"]) })} aria-label={t("users.hiddenMenu")}>
                          {USER_HIDDEN.map((m) => (
                            <FilterItem key={m} value={m} label={t(`users.hiddenModes.${m}`)} />
                          ))}
                        </Menu.RadioGroup>
                      </>
                    ) : null}
                  </Menu.Content>
                </Menu.Portal>
              </Menu.Root>
            ) : null}
          </div>
        </div>
        {extra ? (
          <div className="flex flex-wrap items-center gap-2" role="group" aria-label={t("users.activeFilters")}>
            {search.folder !== undefined ? (
              <ActiveFilter
                label={`${t("users.foldersLabel")}: ${search.folder === "none" ? t("users.foldersNone") : (folderById.get(search.folder)?.name ?? "")}`}
                onClear={() => setFilter({ folder: undefined })}
              />
            ) : null}
            {search.source ? <ActiveFilter label={`${t("users.sourceLabel")}: ${t(`users.sources.${search.source}`)}`} onClear={() => setFilter({ source: undefined })} /> : null}
            {search.hidden ? <ActiveFilter label={t(`users.hiddenModes.${search.hidden}`)} onClear={() => setFilter({ hidden: undefined })} /> : null}
            <button type="button" className="link-btn px-2 text-[13px]" onClick={resetFilters}>
              {t("users.resetFilter")}
            </button>
          </div>
        ) : null}
      </div>
      <section className="card glass reveal overflow-hidden !p-2 md:!pb-0" style={{ "--i": 1 } as React.CSSProperties} aria-label={t("users.listLabel")} aria-busy={users.isPlaceholderData}>
        <QueryBoundary query={users} pending={<TableSkeleton />} title={t("users.loadFailed")}>
          {(data) =>
            data.users_total === 0 ? (
              <EmptyState title={t("users.emptyTitle")} text={t("users.emptyText")}>
                <Button variant="primary" onClick={() => void navigate({ search: (s) => ({ ...s, create: true }) })}>
                  <Plus size={18} aria-hidden /> {t("dashboard.newUser")}
                </Button>
              </EmptyState>
            ) : data.items.length === 0 ? (
              <EmptyState search title={emptyText(search, data.users_total, data.hidden_total).title} text={emptyText(search, data.users_total, data.hidden_total).text}>
                {search.hidden === undefined && data.hidden_total > 0 ? <Button onClick={() => setFilter({ hidden: "show" })}>{t("users.showHidden", { n: data.hidden_total })}</Button> : null}
                <Button onClick={resetFilters}>{t("users.resetFilter")}</Button>
              </EmptyState>
            ) : (
              <>
                {narrow ? (
                  <div className="flex flex-col gap-2">
                    {data.items.map((u) => (
                      <UserCard key={u.id} u={u} tariff={u.tariff_id ? tariffById.get(u.tariff_id) : undefined} folder={u.folder_id ? folderById.get(u.folder_id) : undefined} onOpen={openUser} />
                    ))}
                  </div>
                ) : (
                  <table className="utable">
                    <thead>
                      <tr>
                        <th className="w-10">
                          <input type="checkbox" className="check" checked={allSelected} aria-label={t("users.selectAll")} onChange={() => setSelected(allSelected ? new Set() : new Set(data.items.map((u) => u.id)))} />
                        </th>
                        <th>{t("users.colUser")}</th>
                        <th>{t("users.colTariff")}</th>
                        <th>{t("users.colTraffic")}</th>
                        <th>{t("users.colExpiry")}</th>
                        <th>{t("users.colDevices")}</th>
                        <th>{t("users.colStatus")}</th>
                        <th className="w-12">
                          <span className="sr-only">{t("common.open")}</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.items.map((u) => (
                        <UserRow key={u.id} u={u} tariff={u.tariff_id ? tariffById.get(u.tariff_id) : undefined} folder={u.folder_id ? folderById.get(u.folder_id) : undefined} selected={selected.has(u.id)} binding={binding} onToggle={toggle} onOpen={openUser} />
                      ))}
                    </tbody>
                  </table>
                )}
                <div className="flex items-center justify-between gap-3 border-t border-[var(--hairline)] p-3 text-[13px] text-[var(--ink-500)]">
                  <span>{t("users.shown", { n: num(data.items.length), total: num(data.total) })}</span>
                  <span className="max-md:hidden">{t("users.keyboardHint")}</span>
                </div>
              </>
            )
          }
        </QueryBoundary>
      </section>

      <BulkBar chosen={chosen} clear={clear} folders={folderList} />
      <FoldersDrawer open={managing} onOpenChange={setManaging} />
      <CreateUserDrawer
        open={!!search.create}
        onOpenChange={(v) => void navigate({ search: (s) => ({ ...s, create: v ? true : undefined }) })}
        onCreated={(id) => void navigate({ search: (s) => ({ ...s, create: undefined, user: id }) })}
      />
      <UserDrawer id={search.user} onClose={() => openUser(undefined)} />
    </>
  );
}

function Usage({ u }: { u: User }) {
  const used = u.used_up + u.used_down;
  if (u.traffic_limit == null) {
    return (
      <div className="usage inf min-w-[160px]">
        <div className="usage-txt">
          <span>
            <b className="num">{bytes(used)}</b>
          </span>
          <span>{t("users.unlimited")}</span>
        </div>
        <Bar pct={100} />
      </div>
    );
  }
  const pct = u.traffic_limit > 0 ? (used / u.traffic_limit) * 100 : 100;
  return (
    <div className={clsx("usage min-w-[160px]", pct >= 100 ? "bad" : pct >= 85 && "warn")}>
      <div className="usage-txt">
        <span>
          <b className="num">{bytes(used)}</b> {t("users.of", { total: bytes(u.traffic_limit) })}
        </span>
        <span className="num">{Math.min(100, Math.round(pct))}%</span>
      </div>
      <Bar pct={pct} label={t("users.colTraffic")} />
    </div>
  );
}

function Expiry({ u }: { u: User }) {
  const e = expiryText(u.expires_at);
  return (
    <>
      <div className="font-medium">{u.expires_at ? t("users.until", { date: dateShort(u.expires_at) }) : t("time.forever")}</div>
      {u.expires_at ? <div className={clsx("exp-days", e.tone)}>{e.text}</div> : null}
    </>
  );
}

/**
 * Props of a plain link to a user that opens the drawer without a page load. The router's
 * own Link works out an address per render from a search function, which adds up over 500
 * rows; here the address is the bare query (it resolves against the current page, so the
 * base path needs no care) and a plain click is taken over. A click with a modifier is left
 * to the browser, so "open in a new tab" still works.
 */
function openLink(id: number, onOpen: (id: number) => void) {
  return {
    href: `?user=${id}`,
    onClick: (e: React.MouseEvent) => {
      e.stopPropagation();
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      onOpen(id);
    },
  };
}

// Rows are memoized: a poll hands back the same objects for users that did not change, and
// a tick in one checkbox then redraws that one row. Text is read at render time, so a row
// subscribes to the language itself.
const UserRow = memo(function UserRow({ u, tariff, folder, selected, binding, onToggle, onOpen }: { u: User; tariff?: Tariff; folder?: Folder; selected: boolean; binding: boolean; onToggle: (id: number) => void; onOpen: (id: number) => void }) {
  useLocale();
  // The online mode counts the addresses online at once, not the places taken.
  const devices = binding && u.device_mode !== "online" ? u.bound_devices : u.online_ips.length;
  // The row is clickable for the mouse; the keyboard and screen readers use the link in
  // the name cell and the checkbox, each a control of its own.
  return (
    <tr className={selected ? "sel" : undefined} onClick={() => onOpen(u.id)}>
      <td onClick={(e) => e.stopPropagation()}>
        <input type="checkbox" className="check" checked={selected} onChange={() => onToggle(u.id)} aria-label={t("users.select", { name: u.name })} />
      </td>
      <td>
        <div className="flex min-w-[200px] items-center gap-3">
          <Avatar name={u.name} seed={u.id} />
          <div className="min-w-0">
            <div className="truncate font-medium">
              <a className="row-link" {...openLink(u.id, onOpen)}>
                {u.name}
              </a>
              {u.online ? <span className="online-dot" title={t("users.onlineNow")} role="img" aria-label={t("users.onlineNow")} /> : null}
            </div>
            <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-[var(--ink-500)]">
              {u.contact ? <span>{u.contact}</span> : null}
              {u.tags.map((tag) => (
                <span key={tag} className="tag">
                  {tag}
                </span>
              ))}
              <Labels u={u} folder={folder} />
            </div>
          </div>
        </div>
      </td>
      <td>
        <div className="font-medium">{tariff?.name ?? "—"}</div>
        <div className="mt-0.5 text-xs text-[var(--ink-500)]">{u.traffic_limit != null ? bytes(u.traffic_limit) : t("users.unlimited")}</div>
      </td>
      <td>
        <Usage u={u} />
      </td>
      <td>
        <Expiry u={u} />
      </td>
      <td>
        <span className={clsx("num font-medium", u.device_limit != null && devices >= u.device_limit && devices > 0 && "text-[var(--honey-600)]")}>
          {devices}
          <small className="font-normal text-[var(--ink-500)]"> {t("users.of", { total: u.device_limit ?? "∞" })}</small>
        </span>
      </td>
      <td>
        <StatePill state={u.state} />
      </td>
      <td className="text-right">
        <span className="icon-btn" aria-hidden>
          <ChevronRight size={18} />
        </span>
      </td>
    </tr>
  );
});

const UserCard = memo(function UserCard({ u, tariff, folder, onOpen }: { u: User; tariff?: Tariff; folder?: Folder; onOpen: (id: number) => void }) {
  useLocale();
  const e = expiryText(u.expires_at);
  return (
    <a {...openLink(u.id, onOpen)} className="panel-soft cv-auto grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 p-3 text-left">
      <Avatar name={u.name} seed={u.id} />
      <div className="min-w-0">
        <div className="truncate font-medium">
          {u.name}
          {u.online ? <span className="online-dot" role="img" aria-label={t("users.onlineNow")} /> : null}
        </div>
        <div className="truncate text-xs text-[var(--ink-500)]">
          {tariff?.name ?? t("users.noTariff")} · {e.text}
        </div>
        {u.source !== "admin" || folder || u.hidden ? (
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
            <Labels u={u} folder={folder} />
          </div>
        ) : null}
      </div>
      <StatePill state={u.state} />
      <div className="col-span-3">
        <Usage u={u} />
      </div>
    </a>
  );
});

function TableSkeleton() {
  return (
    <div role="status" aria-busy aria-label={t("users.loadingList")}>
      {Array.from({ length: 8 }, (_, i) => (
        <div key={i} className="grid grid-cols-[40px_2fr_1fr_1.4fr_1fr_0.8fr_0.9fr] items-center gap-3 border-t border-[var(--hairline)] px-3 py-4 first:border-t-0">
          <Skeleton style={{ width: 18, height: 18, borderRadius: 6 }} />
          <div className="flex items-center gap-3">
            <Skeleton style={{ width: 36, height: 36, borderRadius: "50%" }} />
            <div className="flex flex-1 flex-col gap-2">
              <Skeleton style={{ width: "70%" }} />
              <Skeleton style={{ width: "40%" }} />
            </div>
          </div>
          <Skeleton style={{ width: "60%" }} />
          <Skeleton />
          <Skeleton style={{ width: "50%" }} />
          <Skeleton style={{ width: "40%" }} />
          <Skeleton style={{ width: "70%", height: 24, borderRadius: 12 }} />
        </div>
      ))}
    </div>
  );
}

type BulkAction = "extend" | "reset" | "disable" | "enable" | "delete" | "hide" | "unhide" | "move";

/** How many names the delete confirmation lists before it says "…". */
const NAMES_SHOWN = 5;

function BulkBar({ chosen, clear, folders }: { chosen: User[]; clear: () => void; folders: Folder[] }) {
  const toast = useToast();
  const bulk = useUserMutation(userActions.bulk);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const n = chosen.length;
  const busy = bulk.isPending;
  const running = (action: BulkAction) => busy && bulk.variables?.action === action;
  // Hiding is one button that does what the chosen need: bring them back when all of them
  // are hidden, hide them otherwise.
  const unhide = n > 0 && chosen.every((u) => u.hidden);
  const hideAction: BulkAction = unhide ? "unhide" : "hide";
  const run = (action: BulkAction, extra?: { folder_id?: number }) =>
    bulk.mutate(
      { ids: chosen.map((u) => u.id), action, ...extra },
      {
        onSuccess: (r) => {
          toast.ok(t(`users.bulkDone.${action}`, { n: r.affected }));
          clear();
          setConfirmDelete(false);
        },
        onError: (e) => toast.error(errorText(e)),
      },
    );
  // The confirmation of a delete has nothing to confirm once the selection is gone.
  useEffect(() => {
    if (n === 0) setConfirmDelete(false);
  }, [n]);
  const names = chosen
    .slice(0, NAMES_SHOWN)
    .map((u) => u.name)
    .join(", ");
  return (
    <>
      <AnimatePresence>
        {n > 0 ? (
          <motion.div
            className="bulk-bar glass-strong"
            role="region"
            aria-label={t("users.bulkLabel")}
            initial={{ opacity: 0, y: 24 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: 24 }}
            transition={{ type: "spring", stiffness: 420, damping: 32 }}
          >
            <span className="num mr-2 font-semibold whitespace-nowrap">{t("users.selected", { n })}</span>
            <Button size="sm" loading={running("extend")} disabled={busy} onClick={() => run("extend")} title={t("users.extendPeriodHint")}>
              <CalendarPlus size={16} aria-hidden />
              <span className="max-sm:hidden">{t("users.extendPeriod")}</span>
            </Button>
            <Button size="sm" loading={running("reset")} disabled={busy} onClick={() => run("reset")} aria-label={t("users.resetTraffic")}>
              <RotateCcw size={16} aria-hidden />
              <span className="max-sm:hidden">{t("users.resetTraffic")}</span>
            </Button>
            {folders.length > 0 ? (
              <Menu.Root>
                <Menu.Trigger asChild>
                  <Button size="sm" loading={running("move")} disabled={busy} aria-label={t("users.moveMenu")}>
                    <FolderInput size={16} aria-hidden />
                    <span className="max-sm:hidden">{t("users.toFolder")}</span>
                  </Button>
                </Menu.Trigger>
                <Menu.Portal>
                  <Menu.Content className="menu glass-strong" side="top" align="center" sideOffset={8}>
                    <FolderMenuItems folders={folders} onPick={(id) => run("move", id === null ? undefined : { folder_id: id })} />
                  </Menu.Content>
                </Menu.Portal>
              </Menu.Root>
            ) : null}
            <Button size="sm" loading={running(hideAction)} disabled={busy} onClick={() => run(hideAction)} aria-label={unhide ? t("users.unhide") : t("users.hide")} title={unhide ? t("users.unhideHint") : t("users.hideHint")}>
              {unhide ? <Eye size={16} aria-hidden /> : <EyeOff size={16} aria-hidden />}
              <span className="max-sm:hidden">{unhide ? t("users.unhide") : t("users.hide")}</span>
            </Button>
            <Button size="sm" variant="danger" loading={running("disable")} disabled={busy} onClick={() => run("disable")} aria-label={t("users.disable")}>
              <Power size={16} aria-hidden />
              <span className="max-sm:hidden">{t("users.disable")}</span>
            </Button>
            <Button size="sm" variant="danger" disabled={busy} onClick={() => setConfirmDelete(true)} aria-label={t("users.deleteSelected")}>
              <Trash2 size={16} aria-hidden />
            </Button>
            <button type="button" className="icon-btn" aria-label={t("users.clearSelection")} disabled={busy} onClick={clear}>
              <X size={16} />
            </button>
          </motion.div>
        ) : null}
      </AnimatePresence>
      <Confirm
        open={confirmDelete && n > 0}
        onOpenChange={setConfirmDelete}
        title={t("users.deleteTitle", { n })}
        text={`${t("users.deleteText")} ${t("users.deleteWho", { names: n > NAMES_SHOWN ? `${names}…` : names })}`}
        confirm={t("common.delete")}
        danger
        loading={running("delete")}
        onConfirm={() => run("delete")}
      />
    </>
  );
}
