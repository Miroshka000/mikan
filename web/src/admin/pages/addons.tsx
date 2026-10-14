import { useNavigate, useSearch } from "@tanstack/react-router";
import { LayoutGrid, Link2, Puzzle, Wrench } from "lucide-react";
import { useState } from "react";
import { useAddons, useMarketplace } from "../../api/hooks";
import { Tabs } from "../../components/tabs";
import { Button, PageHeader, Spinner } from "../../components/ui";
import { t } from "../../i18n";
import { ADDON_TABS, type AddonsSearch } from "../search";
import { CatalogTab } from "./addons-catalog";
import { InstalledTab, type AddonActions } from "./addons-installed";
import { ApproveDialog, MarketDetails, OwnDrawer } from "./market-dialogs";
import { marketName, marketReady, serverQueue, type Market, type MarketEntry, type MarketInstalled } from "./market-common";
import { PaymentDrawer, addonName } from "./payment-addons";
import { SourcesDrawer } from "./sources-drawer";

type Addons = ReturnType<typeof useAddons>["data"];
type Tab = (typeof ADDON_TABS)[number];

/**
 * The addons: what is installed (the bot, the payment methods, pages and other addons of the
 * marketplace, the tools built into the panel), each in one row with its state and its
 * actions; and the catalog to install from, the signed one and the sources the admin added.
 * The dialogs of both tabs live here.
 */
export function AddonsPage() {
  const search = useSearch({ from: "/_app/addons" });
  const navigate = useNavigate({ from: "/addons" });
  const tab: Tab = search.tab ?? "installed";
  const market = useMarketplace();
  const pay = useAddons();
  const m = market.data;
  const p = pay.data;
  const ready = marketReady(m);
  const [approve, setApprove] = useState<MarketEntry | null>(null);
  const [details, setDetails] = useState<MarketInstalled | null>(null);
  const [payment, setPayment] = useState<string | null>(null);
  const [sourcesOpen, setSourcesOpen] = useState(false);
  // The addon a new build is for stays through the drawer's closing animation.
  const [ownOpen, setOwnOpen] = useState(false);
  const [ownFor, setOwnFor] = useState<MarketInstalled | null>(null);
  const go = (next: AddonsSearch) => void navigate({ search: next, replace: true });
  const act: AddonActions = {
    approve: setApprove,
    details: setDetails,
    payment: setPayment,
    own: (a) => {
      setOwnFor(a);
      setOwnOpen(true);
    },
    catalog: (kind) => go({ tab: "catalog", kind }),
  };
  return (
    <>
      <PageHeader
        title={t("nav.addons")}
        sub={t("addons.subtitle")}
        actions={
          <>
            <Button onClick={() => setSourcesOpen(true)}>
              <Link2 size={16} aria-hidden /> {t("sources.open")}
            </Button>
            <Button onClick={() => act.own(null)} disabled={!ready} title={!ready && m ? t("addons.ownUnavailable") : undefined}>
              <Wrench size={16} aria-hidden /> {t("market.own")}
            </Button>
          </>
        }
      />
      <ServerBanners m={m} p={p} />
      <Tabs
        id="addons"
        label={t("addons.sections")}
        tabs={[
          { id: "installed", label: t("addons.tabs.installed"), icon: Puzzle },
          { id: "catalog", label: t("addons.tabs.catalog"), icon: LayoutGrid },
        ]}
        value={tab}
        onChange={(next) => go(next === "catalog" ? { tab: next, q: search.q, kind: search.kind } : {})}
      >
        {tab === "installed" ? (
          <InstalledTab market={market} pay={pay} act={act} />
        ) : (
          <CatalogTab market={market} pay={pay} act={act} q={search.q ?? ""} kind={search.kind} onSearch={(next) => go({ tab: "catalog", q: search.q, kind: search.kind, ...next })} />
        )}
      </Tabs>
      <ApproveDialog entry={approve} granted={approve ? (m?.installed.find((a) => a.id === approve.id)?.granted ?? []) : []} onClose={() => setApprove(null)} />
      <MarketDetails a={details} name={details ? marketName(details.id, m) : ""} onClose={() => setDetails(null)} />
      <SourcesDrawer open={sourcesOpen} onOpenChange={setSourcesOpen} />
      <OwnDrawer open={ownOpen && ready} onOpenChange={setOwnOpen} update={ownFor} />
      <PaymentDrawer a={(payment && p?.installed.find((a) => a.id === payment)) || null} onClose={() => setPayment(null)} />
    </>
  );
}

/** The name of an addon of either kind, for the server's requests that name it by id. */
function anyName(id: string, m: Market | undefined, p: Addons): string {
  return m && (m.installed.some((a) => a.id === id) || m.catalog.some((e) => e.id === id)) ? marketName(id, m) : addonName(id, p);
}

/**
 * Why nothing can be installed, what the server is doing now, and how its last request went.
 * Both lists read one queue of the server, so each request shows once.
 */
function ServerBanners({ m, p }: { m: Market | undefined; p: Addons }) {
  const queue = serverQueue(m, p);
  const pending = queue[0];
  const queued = Math.max(queue.length - 1, 0);
  const lastOf = m?.last ?? p?.last;
  const last = lastOf && !pending && lastOf.state === "failed" ? lastOf : null;
  return (
    <>
      {(m && !m.supported) || (p && !p.supported) ? (
        <div className="banner warn" role="status">
          {t("market.unsupported")}
        </div>
      ) : m && !m.host_ready ? (
        <div className="banner warn flex-wrap" role="status">
          <span className="min-w-0 flex-1">{t("market.hostOutdated")}</span>
          <code className="mono">mikan update</code>
        </div>
      ) : null}
      {pending ? (
        <div className="banner info" role="status">
          <Spinner />
          <span className="min-w-0 flex-1">
            {t(pending.action === "remove" ? "addons.removing" : "addons.installing", { name: anyName(pending.id, m, p) })}
            {queued ? ` ${t("addons.queuedMore", { n: queued })}` : ""}
          </span>
        </div>
      ) : null}
      {last ? (
        <div className="banner err" role="alert">
          <span className="min-w-0 flex-1 break-words">{t(last.action === "remove" ? "addons.removeFailed" : "addons.installFailed", { name: anyName(last.id, m, p), error: last.error ?? "" })}</span>
        </div>
      ) : null}
    </>
  );
}
