import * as Menu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpCircle, Bot, Info, ListFilter, PackagePlus, RefreshCw, Send, ShieldAlert, ShieldBan, Trash2, Wallet } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useAddons, useBotSlot, useFilters, usePaymentSettings, useTelegram, useTorrent } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { StaleNotice } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, EmptyState, Pill, Skeleton } from "../../components/ui";
import { t, tMaybe, type Key } from "../../i18n";
import { localized } from "../../lib/localized";
import type { AddonKind } from "../search";
import { Group, Item, Row, RowMenu, type State } from "./addon-row";
import { StorageRow } from "./addons-storage";
import { useInstallMarket, useInstallPayment, useRemoveAddon, useRequested } from "./market-dialogs";
import { KIND_ICONS, botStatus, entryOf, hasPages, installMarket, installPayment, marketName, marketReady, pagesPath, queuedIds, type BotPhase, type Market, type MarketEntry, type MarketInstalled } from "./market-common";
import { OriginMark } from "./market-parts";

type Payments = Schemas["AddonsView"];
type Payment = Schemas["AddonView"];
type Telegram = Schemas["TelegramView"];

/** What a row asks the page for: its dialogs and drawers, and the catalog. */
export type AddonActions = {
  approve: (e: MarketEntry) => void;
  details: (a: MarketInstalled) => void;
  payment: (id: string) => void;
  own: (a: MarketInstalled | null) => void;
  catalog: (kind?: AddonKind) => void;
};

/** The official bot addon of the catalog. */
const OFFICIAL_BOT = "telegram";

/**
 * What is installed, one row each, in groups: the bot, the payment methods, pages and other
 * addons, the tools built into the panel. Each list shows as soon as its data is in.
 */
export function InstalledTab({ market, pay, act }: { market: UseQueryResult<Market>; pay: ReturnType<typeof useAddons>; act: AddonActions }) {
  const m = market.data;
  const p = pay.data;
  const tg = useTelegram().data;
  const slotView = useBotSlot().data;
  const slot = slotView?.addon;
  const selling = usePaymentSettings().data?.enabled;
  const queued = queuedIds(m, p);
  const ready = marketReady(m);
  const bots = m?.installed.filter((a) => a.kind === "bot" || a.id === slot) ?? [];
  const others = m?.installed.filter((a) => !bots.includes(a)) ?? [];
  const loading = (!m && !market.isError) || (!p && !pay.isError);
  const empty = !!m && !!p && m.installed.length === 0 && p.installed.length === 0;
  const stale = (market.isError && !!m) || (pay.isError && !!p);
  return (
    <>
      {stale ? (
        <StaleNotice
          onRetry={() => {
            void market.refetch();
            void pay.refetch();
          }}
          retrying={market.isFetching || pay.isFetching}
        />
      ) : null}
      {market.isError && !m ? <LoadError text={t("addons.loadMarketFailed")} error={market.error} onRetry={() => void market.refetch()} retrying={market.isFetching} /> : null}
      {pay.isError && !p ? <LoadError text={t("addons.loadPaymentsFailed")} error={pay.error} onRetry={() => void pay.refetch()} retrying={pay.isFetching} /> : null}
      <Updates m={m} p={p} ready={ready} queued={queued} />
      {tg?.enabled && slotView && !slotView.addon && !slotView.auto ? <NoBotBanner m={m} ready={ready} queued={queued} act={act} /> : null}
      {loading ? <GroupSkeleton /> : null}
      {empty ? (
        <section className="card glass reveal">
          <EmptyState icon={PackagePlus} title={t("market.empty")} text={t("addons.emptyInstalled")}>
            <Button variant="primary" onClick={() => act.catalog()}>
              {t("addons.openCatalog")}
            </Button>
          </EmptyState>
        </section>
      ) : null}
      {m && bots.length ? (
        <Group title={t("addons.groupBot")} i={1}>
          {bots.map((a) => (
            <BotRow key={a.id} a={a} m={m} tg={tg} slot={slot} ready={ready} queued={queued.has(a.id)} act={act} />
          ))}
        </Group>
      ) : null}
      {p && p.installed.length ? (
        <Group title={t("addons.groupPayments")} i={2}>
          {p.installed.map((a) => (
            <PaymentRow key={a.id} a={a} p={p} selling={selling} queued={queued.has(a.id)} act={act} />
          ))}
        </Group>
      ) : null}
      {m && others.length ? (
        <Group title={others.every((a) => a.kind === "ui") ? t("addons.groupPages") : t("addons.groupOther")} i={3}>
          {others.map((a) => (
            <MarketRow key={a.id} a={a} m={m} ready={ready} queued={queued.has(a.id)} act={act} />
          ))}
        </Group>
      ) : null}
      <Group title={t("addons.groupBuiltin")} i={4}>
        <TorrentRow />
        <FiltersRow />
        <StorageRow />
      </Group>
    </>
  );
}

/**
 * The bot is on but the admin removed the addon that answered in it, so nobody does: the
 * panel will not put the official one back by itself. The two ways out, side by side.
 */
function NoBotBanner({ m, ready, queued, act }: { m: Market | undefined; ready: boolean; queued: Set<string>; act: AddonActions }) {
  const qc = useQueryClient();
  const toast = useToast();
  const entry = m ? entryOf(OFFICIAL_BOT, m) : undefined;
  const off = useMutation({
    mutationFn: () => unwrap(api.PATCH("/api/v1/telegram", { body: { enabled: false } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.telegram, v);
      toast.ok(t("addons.botTurnedOff"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <div className="banner warn flex-wrap" role="status">
      <span className="min-w-[min(100%,240px)] flex-1">{t("addons.botOrphan")}</span>
      {entry && !entry.installed ? (
        <Button size="sm" variant="primary" disabled={!ready || queued.has(OFFICIAL_BOT)} onClick={() => act.approve(entry)}>
          {t("addons.botOrphanInstall")}
        </Button>
      ) : null}
      <Button size="sm" loading={off.isPending} onClick={() => off.mutate()}>
        {t("addons.botTurnOff")}
      </Button>
    </div>
  );
}

function LoadError({ text, error, onRetry, retrying }: { text: string; error: unknown; onRetry: () => void; retrying: boolean }) {
  return (
    <div className="banner err flex-wrap" role="alert">
      <span className="min-w-[min(100%,240px)] flex-1 break-words">
        {text}: {errorText(error)}
      </span>
      <Button size="sm" loading={retrying} onClick={onRetry}>
        {t("common.retry")}
      </Button>
    </div>
  );
}

function GroupSkeleton() {
  return (
    <section aria-busy aria-label={t("common.loading")}>
      <Skeleton style={{ height: 16, width: 96, borderRadius: 8, margin: "0 4px 8px" }} />
      <ul className="addon-list card glass">
        {[0, 1, 2].map((i) => (
          <li key={i} className="addon-row">
            <Skeleton style={{ height: 40, width: 40, borderRadius: 12 }} />
            <span className="flex min-w-0 flex-col gap-2">
              <Skeleton style={{ height: 16, width: "40%", borderRadius: 8 }} />
              <Skeleton style={{ height: 12, width: "64%", borderRadius: 8 }} />
            </span>
            <span className="addon-row-side">
              <span className="addon-row-state">
                <Skeleton style={{ height: 24, width: 88, borderRadius: 999 }} />
              </span>
              <span className="addon-row-actions">
                <Skeleton style={{ height: 32, width: 104, borderRadius: 10 }} />
              </span>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Where an addon of the marketplace comes from: checked by mikan, a source's, or the admin's own. */
function SourceMark({ a }: { a: MarketInstalled }) {
  if (a.source === "official") return <span className="tag">{t("addons.official")}</span>;
  if (a.origin) return <OriginMark origin={a.origin} />;
  return (
    <span title={t("market.unverifiedHint")}>
      <Pill tone="warn">{t("market.unverified")}</Pill>
    </span>
  );
}

/** A new build in the catalog: one to take, or one that waits for more permissions. */
function UpdateMark({ entry }: { entry: { wider?: boolean; update?: boolean } | undefined }) {
  if (entry?.wider) return <Pill tone="warn">{t("market.widerReady")}</Pill>;
  if (entry?.update) return <Pill tone="off">{t("market.updateReady")}</Pill>;
  return null;
}

/** The marks by an addon's name: its source (marketplace addons), a new build, a request waiting. */
function AddonMarks({ a, entry, queued }: { a?: MarketInstalled; entry: { wider?: boolean; update?: boolean } | undefined; queued: boolean }) {
  return (
    <>
      {a ? <SourceMark a={a} /> : null}
      <UpdateMark entry={entry} />
      {queued ? <Pill tone="off">{t("addons.queued")}</Pill> : null}
    </>
  );
}

/** The button to the addon's own pages in the admin panel; none while it has none. */
function OpenPagesLink({ a, name }: { a: MarketInstalled; name: string }) {
  if (!hasPages(a)) return null;
  return (
    <Link to={pagesPath(a.id)} className="btn btn-glass btn-sm" aria-label={t("market.openPagesLabel", { name })}>
      {t("market.openPages")}
    </Link>
  );
}

/** Removing an addon of either kind, after the admin has seen what it means; note adds to it. */
function RemoveConfirm({ kind, id, name, note, open, onOpenChange }: { kind: "payment" | "market"; id: string; name: string; note?: string; open: boolean; onOpenChange: (v: boolean) => void }) {
  const drop = useRemoveAddon(kind, () => onOpenChange(false));
  return (
    <Confirm
      open={open}
      onOpenChange={onOpenChange}
      title={t("addons.removeTitle", { name })}
      text={[t(kind === "payment" ? "addons.removeText" : "market.removeText"), note].filter(Boolean).join(" ")}
      confirm={t("addons.remove")}
      danger
      loading={drop.isPending}
      onConfirm={() => drop.mutate(id)}
    />
  );
}

const BOT_WORDS: Record<BotPhase, Key> = { off: "addons.botOff", ok: "addons.botAnswers", reconnecting: "telegram.reconnecting", starting: "telegram.starting", down: "addons.botDown" };

/** Who answers in the bot: the addon holding it, while the panel's bot is set up and runs. */
function botState(holds: boolean, slot: string | undefined, tg: Telegram | undefined): State | undefined {
  if (slot === undefined) return undefined;
  if (!holds) return slot ? { tone: "off", text: t("addons.botElsewhere") } : { tone: "warn", text: t("addons.botWaiting") };
  if (!tg) return undefined;
  if (!tg.token_set) return { tone: "warn", text: t("addons.botNoToken") };
  return botStatus(tg, BOT_WORDS);
}

function BotRow({ a, m, tg, slot, ready, queued, act }: { a: MarketInstalled; m: Market; tg: Telegram | undefined; slot: string | undefined; ready: boolean; queued: boolean; act: AddonActions }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [give, setGive] = useState(false);
  const name = marketName(a.id, m);
  const entry = entryOf(a.id, m);
  const holds = !!slot && slot === a.id;
  const canHold = slot !== undefined && !holds && a.granted.includes("bot");
  const hand = useMutation({
    mutationFn: () => unwrap(api.PUT("/api/v1/telegram/bot-addon", { body: { addon: a.id } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.botSlot, v);
      void qc.invalidateQueries({ queryKey: qk.telegram });
      setGive(false);
      toast.ok(t("addons.giveBotDone", { name }));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const sub = [
    holds && tg?.bot ? `@${tg.bot.username}` : t(`market.kind.${a.kind}`),
    holds && tg ? t("addons.subsN", { n: tg.linked }) : null,
    a.version ? `v${a.version}` : t(`market.source.${a.source}`),
  ]
    .filter(Boolean)
    .join(" · ");
  const error = holds && tg?.enabled && tg.error ? (tMaybe(`telegram.err.${tg.error}`) ?? tg.error) : null;
  const holder = slot ? marketName(slot, m) : "";
  return (
    <Row
      icon={Bot}
      name={name}
      marks={<AddonMarks a={a} entry={entry} queued={queued} />}
      sub={sub}
      error={error}
      state={botState(holds, slot, tg)}
      actions={
        <>
          {canHold ? (
            <Button size="sm" onClick={() => setGive(true)} disabled={queued}>
              <Send size={16} aria-hidden /> {t("addons.giveBot")}
            </Button>
          ) : null}
          <OpenPagesLink a={a} name={name} />
          {holds || a.id === OFFICIAL_BOT ? (
            <Link to="/addons/telegram" search={{ tab: "connect" }} className="btn btn-glass btn-sm">
              {t("addons.configure")}
            </Link>
          ) : null}
        </>
      }
      menu={
        <>
          <MarketMenu a={a} name={name} entry={entry} ready={ready} queued={queued} act={act} removeNote={holds || !slot ? t("addons.removeBotText") : undefined}>
            {canHold ? (
              <Item icon={Send} onSelect={() => setGive(true)}>
                {t("addons.giveBot")}
              </Item>
            ) : null}
          </MarketMenu>
          <Confirm
            open={give}
            onOpenChange={setGive}
            title={t("addons.giveBotTitle", { name })}
            text={holder ? t("addons.giveBotTextFrom", { name, holder }) : t("addons.giveBotText", { name })}
            confirm={t("addons.giveBot")}
            loading={hand.isPending}
            onConfirm={() => hand.mutate()}
          />
        </>
      }
    />
  );
}

/** The menu of an addon of the marketplace with its removal; a bot adds its own items and words. */
function MarketMenu({
  a,
  name,
  entry,
  ready,
  queued,
  act,
  removeNote,
  children,
}: {
  a: MarketInstalled;
  name: string;
  entry: MarketEntry | undefined;
  ready: boolean;
  queued: boolean;
  act: AddonActions;
  removeNote?: string;
  children?: ReactNode;
}) {
  const [remove, setRemove] = useState(false);
  const update = useInstallMarket();
  const busy = queued || !ready || update.isPending;
  return (
    <>
      <RowMenu name={name}>
        {!busy && entry?.wider ? (
          <Item icon={ShieldAlert} onSelect={() => act.approve(entry)}>
            {t("addons.reviewPerms")}
          </Item>
        ) : !busy && entry?.update ? (
          <Item icon={ArrowUpCircle} onSelect={() => update.mutate(entry)}>
            {t("addons.update")}
          </Item>
        ) : null}
        {!busy && a.source === "external" ? (
          <Item icon={RefreshCw} onSelect={() => act.own(a)}>
            {t("addons.newBuild")}
          </Item>
        ) : null}
        {queued ? null : children}
        <Item icon={Info} onSelect={() => act.details(a)}>
          {t("addons.details")}
        </Item>
        {busy ? null : (
          <>
            <Menu.Separator className="menu-sep" />
            <Item icon={Trash2} danger onSelect={() => setRemove(true)}>
              {t("addons.remove")}
            </Item>
          </>
        )}
      </RowMenu>
      <RemoveConfirm kind="market" id={a.id} name={name} note={removeNote} open={remove} onOpenChange={setRemove} />
    </>
  );
}

function MarketRow({ a, m, ready, queued, act }: { a: MarketInstalled; m: Market; ready: boolean; queued: boolean; act: AddonActions }) {
  const name = marketName(a.id, m);
  const entry = entryOf(a.id, m);
  const sub = [t(`market.kind.${a.kind}`), a.version ? `v${a.version}` : t(`market.source.${a.source}`), `${t("market.factPerms")}: ${t("market.permsN", { n: a.granted.length })}`].join(" · ");
  return (
    <Row
      icon={KIND_ICONS[a.kind]}
      name={name}
      marks={<AddonMarks a={a} entry={entry} queued={queued} />}
      sub={sub}
      state={{ tone: "ok", text: t("market.running") }}
      actions={<OpenPagesLink a={a} name={name} />}
      menu={<MarketMenu a={a} name={name} entry={entry} ready={ready} queued={queued} act={act} />}
    />
  );
}

/** A payment method takes payments when it is on, set up, running and selling is on. */
function paymentState(a: Payment, selling: boolean | undefined): State {
  if (a.info_error) return { tone: "bad", text: t("addons.payUnreachable") };
  if (!a.enabled) return { tone: "off", text: t("addons.off") };
  if (!a.available) return { tone: "warn", text: t("addons.payNotSet") };
  if (selling === false) return { tone: "off", text: t("addons.paySalesOff") };
  return { tone: "ok", text: t("addons.payAccepts") };
}

function PaymentRow({ a, p, selling, queued, act }: { a: Payment; p: Payments; selling: boolean | undefined; queued: boolean; act: AddonActions }) {
  const [remove, setRemove] = useState(false);
  const name = localized(a.name, a.id);
  const entry = p.catalog.find((e) => e.id === a.id);
  const install = useInstallPayment();
  const text = localized(entry?.description, "") || t("addons.kindPayment");
  const busy = queued || !p.supported;
  return (
    <Row
      icon={Wallet}
      name={name}
      marks={<AddonMarks entry={entry} queued={queued} />}
      sub={`${text} · v${a.version}`}
      error={a.info_error ? t("addons.unreachable") : null}
      state={paymentState(a, selling)}
      actions={
        <Button size="sm" onClick={() => act.payment(a.id)}>
          {t("addons.configure")}
        </Button>
      }
      menu={
        <>
          <RowMenu name={name}>
            {!busy && entry?.update ? (
              <Item icon={ArrowUpCircle} onSelect={() => install.mutate(a.id)}>
                {t("addons.update")}
              </Item>
            ) : null}
            <Item icon={Wallet} onSelect={() => act.payment(a.id)}>
              {t("addons.configure")}
            </Item>
            {busy ? null : (
              <>
                <Menu.Separator className="menu-sep" />
                <Item icon={Trash2} danger onSelect={() => setRemove(true)}>
                  {t("addons.remove")}
                </Item>
              </>
            )}
          </RowMenu>
          <RemoveConfirm kind="payment" id={a.id} name={name} open={remove} onOpenChange={setRemove} />
        </>
      }
    />
  );
}

function onOff(on: boolean | undefined, failed: boolean): State | undefined {
  if (on === undefined) return failed ? { tone: "bad", text: t("common.loadFailed") } : undefined;
  return on ? { tone: "ok", text: t("addons.on") } : { tone: "off", text: t("addons.off") };
}

function TorrentRow() {
  const q = useTorrent();
  return (
    <Row
      icon={ShieldBan}
      name={t("settings.torrent.title")}
      sub={t("addons.torrentSub")}
      state={onOff(q.data?.enabled, q.isError)}
      actions={
        <Link to="/addons/torrent" className="btn btn-glass btn-sm">
          {t("addons.configure")}
        </Link>
      }
    />
  );
}

function FiltersRow() {
  const q = useFilters();
  const on = q.data ? q.data.egress.enabled || q.data.ingress.enabled : undefined;
  return (
    <Row
      icon={ListFilter}
      name={t("filters.title")}
      sub={t("addons.filtersSub")}
      state={onOff(on, q.isError)}
      actions={
        <Link to="/addons/filters" className="btn btn-glass btn-sm">
          {t("addons.configure")}
        </Link>
      }
    />
  );
}

type Update = { id: string; name: string; payment: boolean; entry?: MarketEntry };

/**
 * New builds in the catalog, all in one place: the ones that ask for nothing new go with one
 * button; one that asks for more waits for its approval in its row, never sent silently.
 */
function Updates({ m, p, ready, queued }: { m: Market | undefined; p: Payments | undefined; ready: boolean; queued: Set<string> }) {
  const qc = useQueryClient();
  const toast = useToast();
  const requested = useRequested();
  const list: Update[] = [
    ...(p?.catalog ?? []).filter((e) => e.installed && e.update && !queued.has(e.id)).map((e) => ({ id: e.id, name: localized(e.name, e.id), payment: true })),
    ...(m?.installed ?? [])
      .map((a) => entryOf(a.id, m))
      .filter((e): e is MarketEntry => !!e && e.update && !queued.has(e.id))
      .map((e) => ({ id: e.id, name: localized(e.name, e.id), payment: false, entry: e })),
  ];
  const wider = list.filter((u) => u.entry?.wider).length;
  const silent = list.filter((u) => (u.payment ? !!p?.supported : ready && !u.entry?.wider));
  const all = useMutation({
    mutationFn: async (items: Update[]) => {
      let failed = 0;
      for (const u of items) {
        try {
          if (u.payment) await installPayment(u.id);
          else if (u.entry) await installMarket(u.entry);
        } catch {
          failed++;
        }
      }
      return failed;
    },
    onSuccess: (failed, items) => {
      if (failed === items.length) {
        toast.error(t("addons.updateFailed", { n: failed }));
        void qc.invalidateQueries({ queryKey: qk.addons });
        void qc.invalidateQueries({ queryKey: qk.marketplace });
      } else {
        requested();
        if (failed) toast.error(t("addons.updateFailed", { n: failed }));
      }
    },
  });
  if (!list.length) return null;
  return (
    <div className="banner info flex-wrap" role="status">
      <ArrowUpCircle size={16} className="shrink-0" aria-hidden />
      <span className="min-w-[min(100%,240px)] flex-1">
        {t("addons.updatesN", { n: list.length })}: {list.map((u) => u.name).join(", ")}.{wider ? ` ${t("addons.updatesWider", { n: wider })}` : ""}
      </span>
      {silent.length ? (
        <Button size="sm" variant="primary" className="ml-auto" loading={all.isPending} onClick={() => all.mutate(silent)}>
          {silent.length === list.length ? t("addons.updateAll") : t("addons.updateRest")}
        </Button>
      ) : null}
    </div>
  );
}
