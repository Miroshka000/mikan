import type { UseQueryResult } from "@tanstack/react-query";
import { ExternalLink, Search, Store, Wallet } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { errorText, type Schemas } from "../../api/client";
import type { useAddons } from "../../api/hooks";
import { Button, EmptyState, ErrorState, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { num } from "../../lib/format";
import { localized } from "../../lib/localized";
import { safeHref } from "../../lib/url";
import { ADDON_KINDS, type AddonKind } from "../search";
import type { AddonActions } from "./addons-installed";
import { useInstallMarket, useInstallPayment, permLabel } from "./market-dialogs";
import { KIND_ICONS, actsAsAdmin, marketReady, queuedIds, sortPermissions, type Kind, type Market, type MarketEntry } from "./market-common";
import { OriginMark } from "./market-parts";

type ItemKind = Kind | "payment";

/** One entry of either catalog, as a card shows it. */
type Item = {
  id: string;
  kind: ItemKind;
  name: string;
  text: string;
  version: string;
  homepage: string;
  installed: boolean;
  update: boolean;
  /** Protocol 2 only: what it asks for, and the entry the approval shows. */
  market?: MarketEntry;
};

const KIND_ORDER: ItemKind[] = ["bot", "payment", "ui", "generic"];

function kindLabel(k: ItemKind): string {
  return k === "payment" ? t("addons.kindPayment") : t(`market.kind.${k}`);
}

function toItem(e: Schemas["AddonCatalogEntry"] | MarketEntry, kind: ItemKind, market?: MarketEntry): Item {
  return { id: e.id, kind, name: localized(e.name, e.id), text: localized(e.description, ""), version: e.version, homepage: e.homepage, installed: e.installed, update: e.update, market };
}

/**
 * Both catalogs in one grid, the payment methods and the addons of protocol 2: searched and
 * filtered by kind (both kept in the URL), each card with what installing it means.
 */
export function CatalogTab({
  market,
  pay,
  act,
  q,
  kind,
  onSearch,
}: {
  market: UseQueryResult<Market>;
  pay: ReturnType<typeof useAddons>;
  act: AddonActions;
  q: string;
  kind: AddonKind | undefined;
  onSearch: (next: { q?: string; kind?: AddonKind }) => void;
}) {
  const m = market.data;
  const p = pay.data;
  const [text, setText] = useState(q);
  // The last text this tab put into the URL: another one came from outside (Back, a link).
  const written = useRef(q);
  // The page hands a new function on every render; the timer below must not restart on it.
  const search = useRef(onSearch);
  useEffect(() => {
    search.current = onSearch;
  });
  useEffect(() => {
    if (q !== written.current) {
      written.current = q;
      setText(q);
    }
  }, [q]);
  useEffect(() => {
    if (text === written.current) return;
    const timer = window.setTimeout(() => {
      written.current = text;
      search.current({ q: text.trim() ? text : undefined });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [text]);

  const payFailed = (pay.isError && !p) || !!p?.catalog_error;
  const marketFailed = (market.isError && !m) || !!m?.catalog_error;
  const loading = (!p && !pay.isError) || (!m && !market.isError);
  const retry = () => {
    void pay.refetch();
    void market.refetch();
  };
  const queued = queuedIds(m, p);
  const items: Item[] = [...(p?.catalog ?? []).map((e) => toItem(e, "payment")), ...(m?.catalog ?? []).map((e) => toItem(e, e.kind, e))].sort((a, b) => KIND_ORDER.indexOf(a.kind) - KIND_ORDER.indexOf(b.kind) || a.name.localeCompare(b.name));
  const needle = q.trim().toLowerCase();
  const found = needle ? items.filter((i) => [i.name, i.text, i.id, kindLabel(i.kind), i.market?.origin?.title ?? ""].some((s) => s.toLowerCase().includes(needle))) : items;
  const shown = kind ? found.filter((i) => i.kind === kind) : found;
  const counts = (k?: AddonKind) => (k ? found.filter((i) => i.kind === k).length : found.length);

  return (
    <>
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div className="-mx-3 flex items-center gap-2 overflow-x-auto px-3 pb-1 [scrollbar-width:none] lg:mx-0 lg:flex-wrap lg:overflow-visible lg:px-0 lg:pb-0 [&::-webkit-scrollbar]:hidden" role="group" aria-label={t("addons.filterKind")}>
          {([undefined, ...ADDON_KINDS] as const).map((k) => (
            <button key={k ?? "all"} type="button" className="chip shrink-0" aria-pressed={kind === k} onClick={() => onSearch({ kind: k })}>
              {k ? t(`addons.kinds.${k}`) : t("addons.kinds.all")}
              {loading ? null : <span className="chip-count num">{num(counts(k))}</span>}
            </button>
          ))}
        </div>
        <label className="search-field lg:max-w-[360px]">
          <Search size={16} aria-hidden />
          <input type="search" placeholder={t("addons.searchPlaceholder")} value={text} onChange={(e) => setText(e.target.value)} aria-label={t("addons.searchLabel")} />
        </label>
      </div>
      {payFailed !== marketFailed && !loading ? (
        <div className="banner warn flex-wrap" role="status">
          <span className="min-w-[min(100%,240px)] flex-1">
            {payFailed ? t("addons.partialPayments") : t("addons.partialMarket")}
            {pay.isError && !p ? ` ${errorText(pay.error)}` : market.isError && !m ? ` ${errorText(market.error)}` : ""}
          </span>
          <Button size="sm" loading={pay.isFetching || market.isFetching} onClick={retry}>
            {t("common.retry")}
          </Button>
        </div>
      ) : null}
      {loading ? (
        <CatalogSkeleton />
      ) : payFailed && marketFailed ? (
        <section className="card glass">
          <ErrorState text={t("errors.api.catalog_unavailable")} onRetry={pay.isFetching || market.isFetching ? undefined : retry} />
        </section>
      ) : items.length === 0 ? (
        <section className="card glass">
          <EmptyState icon={Store} title={t("addons.catalogEmpty")} text={t("market.catalogEmptyText")} />
        </section>
      ) : shown.length === 0 ? (
        <section className="card glass">
          <EmptyState search title={t("addons.nothingFound")} text={t("addons.nothingFoundText")}>
            <Button
              onClick={() => {
                setText("");
                written.current = "";
                onSearch({ q: undefined, kind: undefined });
              }}
            >
              {t("addons.resetSearch")}
            </Button>
          </EmptyState>
        </section>
      ) : (
        <ul className="addon-catalog" aria-label={t("addons.tabs.catalog")}>
          {shown.map((i, n) => (
            <CatalogCard key={`${i.kind === "payment" ? "p" : "m"}-${i.id}`} item={i} index={n} m={m} p={p} queued={queued.has(i.id)} act={act} />
          ))}
        </ul>
      )}
    </>
  );
}

function CatalogSkeleton() {
  return (
    <ul className="addon-catalog" aria-busy aria-label={t("common.loading")}>
      {[0, 1, 2, 3, 4, 5].map((i) => (
        <li key={i} className="addon-card card glass">
          <div className="flex items-center gap-3">
            <Skeleton style={{ height: 40, width: 40, borderRadius: 12 }} />
            <span className="flex min-w-0 flex-1 flex-col gap-2">
              <Skeleton style={{ height: 16, width: "60%", borderRadius: 8 }} />
              <Skeleton style={{ height: 12, width: "40%", borderRadius: 8 }} />
            </span>
          </div>
          <Skeleton style={{ height: 32, borderRadius: 8 }} />
          <Skeleton style={{ height: 32, width: 120, borderRadius: 10, marginTop: "auto" }} />
        </li>
      ))}
    </ul>
  );
}

function CatalogCard({ item: i, index, m, p, queued, act }: { item: Item; index: number; m: Market | undefined; p: ReturnType<typeof useAddons>["data"]; queued: boolean; act: AddonActions }) {
  const install = useInstallPayment();
  const update = useInstallMarket();
  const Icon = i.kind === "payment" ? Wallet : KIND_ICONS[i.kind];
  const href = safeHref(i.homepage);
  const e = i.market;
  const blocked = i.kind === "payment" ? !p?.supported : !marketReady(m);
  const busy = install.isPending || update.isPending;
  const act1 = () => {
    if (!e) install.mutate(i.id);
    else if (!i.installed || e.wider) act.approve(e);
    else update.mutate(e);
  };
  return (
    <li className="addon-card card glass reveal" style={{ "--i": Math.min(index, 8) } as React.CSSProperties}>
      <div className="flex items-start gap-3">
        <span className="addon-row-icon" aria-hidden>
          <Icon size={20} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="addon-card-title">{i.name}</h3>
          <div className="mt-0.5 text-xs text-[var(--ink-500)]">
            {kindLabel(i.kind)} · <span className="num">v{i.version}</span>
          </div>
        </div>
      </div>
      {i.text ? <p className="addon-card-text">{i.text}</p> : null}
      <div className="flex flex-wrap items-center gap-1">
        {e?.origin ? (
          <OriginMark origin={e.origin} />
        ) : (
          <span className="tag">{t("addons.official")}</span>
        )}
        {e ? (
          <span title={e.permissions.length ? sortPermissions(e.permissions).map(permLabel).join(", ") : undefined}>
            <Pill tone={actsAsAdmin(e.permissions) ? "warn" : "off"}>{t("addons.permsBadge", { n: e.permissions.length })}</Pill>
          </span>
        ) : null}
        {href ? (
          <a className="link-btn ml-1 inline-flex items-center gap-1 text-xs" href={href} target="_blank" rel="noopener noreferrer">
            {t("addons.homepage")} <ExternalLink size={12} aria-hidden />
          </a>
        ) : null}
      </div>
      <div className="addon-card-foot">
        {i.installed && !i.update ? (
          <>
            <Pill tone="ok">{t("addons.installed")}</Pill>
            {i.kind === "payment" ? (
              <Button size="sm" onClick={() => act.payment(i.id)}>
                {t("addons.configure")}
              </Button>
            ) : null}
          </>
        ) : (
          <>
            {i.installed ? <Pill tone={e?.wider ? "warn" : "off"}>{e?.wider ? t("market.widerReady") : t("market.updateReady")}</Pill> : <span />}
            <Button
              size="sm"
              variant={i.installed ? "glass" : "primary"}
              loading={busy || queued}
              disabled={blocked}
              onClick={act1}
              aria-label={`${i.installed ? (e?.wider ? t("addons.reviewPerms") : t("addons.update")) : t("addons.install")}: ${i.name}`}
            >
              {queued ? t("addons.queued") : i.installed ? (e?.wider ? t("addons.reviewPerms") : t("addons.update")) : t("addons.install")}
            </Button>
          </>
        )}
      </div>
    </li>
  );
}
