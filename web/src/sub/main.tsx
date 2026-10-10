import "../styles/app.css";
import "./look.css";
import { StrictMode, useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { ErrorBoundary } from "../components/error-boundary";
import { LangSwitch } from "../components/lang";
import { Button, Skeleton } from "../components/ui";
import { initI18n, t, useLocale } from "../i18n";
import { subDicts } from "../i18n/sub";
import { APPS } from "./apps";
import { hashParams, initData, json, openOutside, pageURL, request, subRoot, tgEvent, tgMode, tokenOf } from "./net";
import { applyLook, readPage, titleOf, type DocView } from "./page";
import { loadShop, Shop, type ShopData } from "./shop";
import { PromoSection } from "./promo";
import type { Info, TgSub } from "./types";
import { DocScreen, PageBackdrop, PageBlocks, PageShell } from "./view";

/** Pauses between the reloads after a payment: the panel applies a paid one within seconds. */
const AFTER_PAYMENT_MS = [2_000, 5_000, 10_000];

type Insets = { top?: number; bottom?: number; left?: number; right?: number };

/**
 * Telegram's two safe areas: the device's (notch, home bar) and, in full screen, the room
 * its own buttons take on top of it. Their sums go to --tg-top and --tg-bottom on <html>,
 * which the page's padding takes when they exceed the browser's own safe area.
 */
const insets: { safe?: Insets; content?: Insets } = {};
function applyInsets() {
  const root = document.documentElement.style;
  const sum = (side: "top" | "bottom") => (insets.safe?.[side] ?? 0) + (insets.content?.[side] ?? 0);
  root.setProperty("--tg-top", `${sum("top")}px`);
  root.setProperty("--tg-bottom", `${sum("bottom")}px`);
}

// What the admin made of the page, in its HTML: the look goes on before the first paint.
const page = readPage();
// The subscription path, as the server's <base> gives it: the images and instructions are
// under it, whether the page is opened by its token or as the Mini App (/tg).
const pageRoot = new URL(".", document.baseURI).href.replace(/\/$/, "");
const asset = (path?: string) => (path ? pageRoot + "/" + path : undefined);
applyLook(page, asset);
const config = page.config;

function SubPage() {
  // Texts are read at render time: follow a language switch without remounting the page
  // (a remount would sign in to the Mini App and fetch everything again).
  useLocale();
  // The info that came for one subscription's address; it is shown only while that address
  // is the one on screen, so a late answer of another subscription never takes its place.
  const [info, setInfo] = useState<{ url: string; data: Info } | null>(null);
  const [failedURL, setFailedURL] = useState<string | null>(null);
  const [subURL, setSubURL] = useState(tgMode ? "" : pageURL);
  const [tg, setTg] = useState<{ state: "loading" | "none" | "failed" | "ok"; subs: TgSub[] }>({ state: tgMode ? "loading" : "ok", subs: [] });
  const [shop, setShop] = useState<ShopData | null>(null);
  const [packages, setPackages] = useState<{ token: string; data: ShopData } | null>(null);
  const [promoCode, setPromoCode] = useState("");
  // The instruction open on its own screen; the browser's (or Telegram's) back closes it.
  // A reload keeps the instruction that was open (its id is in the history entry).
  const [docId, setDocId] = useState<number | null>(() => {
    const id = (history.state as { doc?: unknown } | null)?.doc;
    return typeof id === "number" ? id : null;
  });
  const docScroll = useRef(0);
  const current = info && info.url === subURL ? info.data : null;
  const failed = !current && failedURL === subURL;

  const loadInfo = useCallback(async (url: string, signal?: AbortSignal) => {
    const d = await request(url + "/info", { signal }).then((r) => json<Info>(r));
    setInfo({ url, data: d });
    setFailedURL(null);
    document.title = d.brand;
  }, []);

  // The Mini App signs in with the launch data Telegram puts after the #.
  const session = useCallback(
    () =>
      request(subRoot + "/tg/session", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ init_data: initData }) })
        .then((r) => json<{ subs: TgSub[] }>(r))
        .then((d) => {
          if (d.subs.length === 0) {
            setTg({ state: "none", subs: [] });
            return;
          }
          setTg({ state: "ok", subs: d.subs });
          // A new subscription bought here shows at once; the one on screen stays otherwise.
          setSubURL((cur) => (cur && d.subs.some((s) => cur.endsWith("/" + s.token)) ? cur : subRoot + "/" + d.subs[0]!.token));
        })
        // A refresh that fails leaves what is on screen; only a first sign-in shows the error.
        .catch(() => setTg((cur) => (cur.state === "ok" && cur.subs.length ? cur : { state: "failed", subs: [] }))),
    [],
  );

  const token = tokenOf(subURL);
  // What a refresh reloads: the session, the subscription's info and the shop.
  const refresh = () => {
    void session();
    if (subURL) void loadInfo(subURL).catch(() => undefined);
    if (tgMode) {
      loadShop(subRoot, initData)
        .then(setShop)
        .catch(() => undefined);
      if (subURL) {
        loadShop(subRoot, initData, token)
          .then((data) => setPackages({ token, data }))
          .catch(() => undefined);
      }
    }
  };
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    if (!tgMode) return;
    tgEvent("web_app_ready");
    tgEvent("web_app_expand");
    // In full screen Telegram draws its own buttons over the top of the page: it tells how
    // much room they take when asked, and again whenever that changes.
    tgEvent("web_app_request_safe_area");
    tgEvent("web_app_request_content_safe_area");
    void session();
    loadShop(subRoot, initData)
      .then(setShop)
      .catch(() => setShop(null));
  }, [session]);

  // Telegram tells the Mini App when its payment sheet closes; a paid one is applied by
  // the panel within seconds, so the page reloads a few times, a little later each time.
  useEffect(() => {
    if (!tgMode) return;
    let timers: number[] = [];
    const onEvent = (type: string, data: unknown) => {
      if (type === "safe_area_changed" || type === "content_safe_area_changed") {
        insets[type === "safe_area_changed" ? "safe" : "content"] = data as Insets;
        applyInsets();
        return;
      }
      if (type === "fullscreen_changed") {
        tgEvent("web_app_request_safe_area");
        tgEvent("web_app_request_content_safe_area");
        return;
      }
      if (type === "back_button_pressed") {
        history.back();
        return;
      }
      if (type === "invoice_closed" && (data as { status?: string } | null)?.status === "paid") {
        timers.forEach((id) => window.clearTimeout(id));
        timers = AFTER_PAYMENT_MS.map((ms) => window.setTimeout(() => refreshRef.current(), ms));
      }
    };
    const w = window as Window & { Telegram?: { WebView?: { receiveEvent?: (type: string, data: unknown) => void } } };
    w.Telegram ??= {};
    w.Telegram.WebView ??= {};
    const prev = w.Telegram.WebView.receiveEvent;
    w.Telegram.WebView.receiveEvent = (type, data) => {
      prev?.(type, data);
      onEvent(type, data);
    };
    const onMessage = (e: MessageEvent) => {
      if (e.origin !== "https://web.telegram.org" || typeof e.data !== "string") return;
      try {
        const m = JSON.parse(e.data) as { eventType?: string; eventData?: unknown };
        if (m.eventType) onEvent(m.eventType, m.eventData);
      } catch {
        // not Telegram's
      }
    };
    window.addEventListener("message", onMessage);
    return () => {
      window.removeEventListener("message", onMessage);
      timers.forEach((id) => window.clearTimeout(id));
      if (w.Telegram?.WebView) w.Telegram.WebView.receiveEvent = prev;
    };
  }, []);

  // One subscription's info (and, in the Mini App, its traffic packages) at a time: a
  // switch cancels what the previous one still waits for.
  useEffect(() => {
    if (!subURL) return;
    const ctl = new AbortController();
    loadInfo(subURL, ctl.signal).catch(() => {
      if (!ctl.signal.aborted) setFailedURL(subURL);
    });
    if (tgMode) {
      const tok = tokenOf(subURL);
      loadShop(subRoot, initData, tok)
        .then((data) => !ctl.signal.aborted && setPackages({ token: tok, data }))
        .catch(() => !ctl.signal.aborted && setPackages(null));
    }
    return () => ctl.abort();
  }, [subURL, loadInfo]);

  // The Mini App sends an app's "Add" to the browser as #open=<app>: open it once, right
  // away. The mark is dropped from the address, or a reload and every refresh of the info
  // would ask the system to open the app again.
  const launched = useRef(false);
  useEffect(() => {
    if (tgMode || !current || launched.current) return;
    const want = hashParams.get("open");
    if (!want) return;
    launched.current = true;
    history.replaceState(null, "", location.pathname + location.search);
    const app = Object.values(APPS)
      .flat()
      .find((a) => a.name === want);
    if (app) location.href = app.link(subURL, current.brand, current.happ_link);
  }, [current, subURL]);

  // An instruction opens as a step of the history: back (the browser's, the phone's or
  // Telegram's button) returns to the page where it was left.
  useEffect(() => {
    const onPop = (e: PopStateEvent) => {
      const id = (e.state as { doc?: number } | null)?.doc;
      setDocId(typeof id === "number" ? id : null);
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  const prevDoc = useRef<number | null>(null);
  useEffect(() => {
    if (tgMode && (docId !== null || prevDoc.current !== null)) tgEvent("web_app_setup_back_button", { is_visible: docId !== null });
    if (docId !== null) window.scrollTo(0, 0);
    else if (prevDoc.current !== null) {
      // Back on the page: where it was, with the focus on the instruction just read.
      const from = prevDoc.current;
      window.requestAnimationFrame(() => {
        window.scrollTo(0, docScroll.current);
        document.getElementById(`doc-${from}`)?.focus({ preventScroll: true });
      });
    }
    prevDoc.current = docId;
  }, [docId]);
  const openDoc = (id: number) => {
    docScroll.current = window.scrollY;
    history.pushState({ doc: id }, "");
    setDocId(id);
  };

  const shopFor = (shopToken: string, title: string) =>
    shop && shop.offers.length > 0 ? (
      <Shop
        data={shop}
        offers={shop.offers}
        subRoot={subRoot}
        initData={initData}
        token={shopToken}
        title={title}
        openInvoice={(slug) => tgEvent("web_app_open_invoice", { slug })}
        openLink={openOutside}
        onRefresh={refresh}
        promoCode={promoCode}
      />
    ) : null;
  // Traffic packages are for the subscription on screen.
  const packagesShop =
    packages && packages.data.packages?.length && packages.token === token ? (
      <Shop
        key={token}
        data={packages.data}
        offers={packages.data.packages}
        field="package_id"
        pick={t("sub.packagesPick")}
        subRoot={subRoot}
        initData={initData}
        token={token}
        title={t("sub.packages")}
        openInvoice={(slug) => tgEvent("web_app_open_invoice", { slug })}
        openLink={openOutside}
        onRefresh={refresh}
        promoCode={promoCode}
      />
    ) : null;

  if (tg.state === "none" && shop?.allow_new && shop.offers.length > 0) {
    return (
      <Shell>
        <section className="glass sub-card p-6 text-center">
          <h1 className="font-display text-xl font-medium">{t("sub.tgNoSubTitle")}</h1>
          <p className="mt-2 text-[13px] text-[var(--ink-500)]">{t("sub.tgNoSubText")}</p>
        </section>
        <PromoSection token="" activeCode={promoCode} onApplied={(c) => setPromoCode(c)} onBonusApplied={refresh} title={titleOf(config, "promo")} />
        {shopFor("", t("sub.shopNew"))}
      </Shell>
    );
  }
  if (tg.state === "none" || tg.state === "failed") {
    return (
      <Shell>
        <section className="glass sub-card p-6 text-center" role={tg.state === "failed" ? "alert" : undefined}>
          <h1 className="font-display text-xl font-medium">{tg.state === "none" ? t("sub.tgNoSubTitle") : t("sub.loadFailed")}</h1>
          <p className="mt-2 text-[13px] text-[var(--ink-500)]">{tg.state === "none" ? t("sub.tgNoSubText") : t("sub.tgFailed")}</p>
          {tg.state === "failed" ? (
            <Button variant="primary" className="mt-4" onClick={() => void session()}>
              {t("common.retry")}
            </Button>
          ) : null}
        </section>
      </Shell>
    );
  }
  if (failed) {
    return (
      <Shell>
        <section className="glass sub-card p-6 text-center" role="alert">
          <h1 className="font-display text-xl font-medium">{t("sub.loadFailed")}</h1>
          <p className="mt-2 text-[13px] text-[var(--ink-500)]">{t("sub.loadFailedText")}</p>
          <Button
            variant="primary"
            className="mt-4"
            onClick={() => {
              setFailedURL(null);
              loadInfo(subURL).catch(() => setFailedURL(subURL));
            }}
          >
            {t("common.retry")}
          </Button>
        </section>
      </Shell>
    );
  }
  if (!current) {
    return (
      <Shell>
        {tgMode ? (
          <p className="px-1 text-[13px] text-[var(--ink-500)]" role="status">
            {t("sub.tgLoading")}
          </p>
        ) : null}
        <Skeleton className="sub-card" style={{ height: 96 }} />
        <Skeleton className="sub-card" style={{ height: 140 }} />
        <Skeleton className="sub-card" style={{ height: 220 }} />
      </Shell>
    );
  }

  if (docId !== null) {
    return (
      <Shell brand={current.brand}>
        <DocScreen
          key={docId}
          item={page.docs.find((d) => d.id === docId)}
          load={() => request(`${subURL}/docs/${docId}`).then((r) => json<DocView>(r))}
          onBack={() => history.back()}
          tgMode={tgMode}
        />
      </Shell>
    );
  }

  return (
    <Shell brand={current.brand}>
      {tg.subs.length > 1 ? (
        <div className="flex gap-1 overflow-x-auto rounded-[14px] bg-[var(--hover)] p-1" role="group" aria-label={t("sub.link")}>
          {tg.subs.map((s) => (
            <button
              key={s.token}
              type="button"
              aria-pressed={subURL.endsWith("/" + s.token)}
              onClick={() => setSubURL(subRoot + "/" + s.token)}
              className="h-8 shrink-0 rounded-[10px] px-3 text-xs font-semibold text-[var(--ink-600)] aria-pressed:bg-[var(--surface-solid)] aria-pressed:text-[var(--ink-900)] aria-pressed:shadow-sm"
            >
              {s.name}
            </button>
          ))}
        </div>
      ) : null}
      <PageBlocks
        config={config}
        info={current}
        subURL={subURL}
        tgMode={tgMode}
        docs={page.docs}
        selling={tgMode && !!shop?.offers.length}
        promo={tgMode ? <PromoSection token={subURL} activeCode={promoCode} onApplied={(c) => setPromoCode(c)} onBonusApplied={refresh} title={titleOf(config, "promo")} /> : null}
        shop={
          tgMode ? (
            <>
              {shopFor(token, titleOf(config, "shop") || t("sub.shop"))}
              {packagesShop}
            </>
          ) : null
        }
        reload={() => loadInfo(subURL).catch(() => undefined)}
        onOpenDoc={openDoc}
      />
    </Shell>
  );
}

function Shell({ brand, children }: { brand?: string; children: React.ReactNode }) {
  return (
    <PageShell config={config} brand={brand ?? page.brand} logo={asset(page.logo)} right={<LangSwitch className="ml-auto" />}>
      {children}
    </PageShell>
  );
}

// Dictionaries load before the first render: t() stays synchronous everywhere.
void initI18n(subDicts).then(() => {
  // The tab's title until the subscription says its brand (sub.html's own is Russian).
  document.title = t("sub.pageTitle");
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ErrorBoundary>
        <PageBackdrop look={config.look} image={asset(page.background)} />
        <SubPage />
      </ErrorBoundary>
    </StrictMode>,
  );
});
