// The subscription page as the editor's draft makes it, in a frame of its own: the page's
// look and the admin's CSS stay inside it (they would restyle the panel otherwise), and the
// frame is as wide as a phone or a laptop, scaled down to the column it sits in.
import "../../../sub/look.css";
import { useDeferredValue, useEffect, useLayoutEffect, useRef, useState, type MouseEvent } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Button } from "../../../components/ui";
import { getLocale, t, useLocale } from "../../../i18n";
import type { Platform } from "../../../sub/apps";
import { cleanCSS, paintLook, titleOf, type DocItem, type DocView, type PageConfig } from "../../../sub/page";
import { ReissueSection } from "../../../sub/reissue";
import { Shop, type ShopData } from "../../../sub/shop";
import type { Info } from "../../../sub/types";
import { DocScreen, PageBackdrop, PageBlocks, PageShell } from "../../../sub/view";

export type PreviewDevice = "phone" | "desktop";

type PreviewProps = {
  config: PageConfig;
  brand: string;
  /** The brand's colour when the page takes it. */
  accent?: string;
  logo?: string;
  background?: string;
  docs: DocView[];
  info: Info;
  subURL: string;
  device: PreviewDevice;
  scheme: "light" | "dark";
  /** Shown as the Mini App: the shop and promo codes are there. */
  tg: boolean;
  platform: Platform;
  shop: ShopData;
};

const SIZE: Record<PreviewDevice, { w: number; h: number }> = { phone: { w: 390, h: 780 }, desktop: { w: 1024, h: 720 } };

// The frame's own page: the panel's styles are copied in once it has loaded.
const FRAME_HTML = '<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><div id="root"></div></body></html>';

export function PagePreview(props: PreviewProps) {
  const stage = useRef<HTMLDivElement>(null);
  const frame = useRef<HTMLIFrameElement>(null);
  const root = useRef<Root | null>(null);
  const [ready, setReady] = useState(false);
  const locale = useLocale();
  const [width, setWidth] = useState(0);
  const [viewport, setViewport] = useState(() => window.innerHeight);
  const size = SIZE[props.device];
  // A phone as tall as the window leaves room for; the laptop at its own proportions.
  const h = props.device === "phone" ? Math.max(560, Math.min(size.h, viewport - 200)) : size.h;
  const scale = width ? Math.min(1, width / size.w) : 0;

  useLayoutEffect(() => {
    const el = stage.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(e?.contentRect.width ?? 0));
    ro.observe(el);
    const onResize = () => setViewport(window.innerHeight);
    window.addEventListener("resize", onResize);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", onResize);
    };
  }, []);

  const init = () => {
    const doc = frame.current?.contentDocument;
    if (!doc || !doc.getElementById("root") || root.current) return;
    for (const n of document.querySelectorAll('link[rel="stylesheet"], style')) doc.head.appendChild(n.cloneNode(true));
    const own = doc.createElement("style");
    own.id = "mikan-css";
    doc.head.appendChild(own);
    root.current = createRoot(doc.getElementById("root")!);
    setReady(true);
  };

  useEffect(
    () => () => {
      const r = root.current;
      root.current = null;
      // Not while React renders this tree: the frame's root goes once it is done.
      if (r) window.setTimeout(() => r.unmount());
    },
    [],
  );

  // Three effects, each on what it paints: a key typed into a text of the page redraws the
  // page, not the look or the admin's CSS, which the frame would re-style for nothing.
  const { look, css } = props.config;
  useEffect(() => {
    const doc = frame.current?.contentDocument;
    if (!ready || !doc) return;
    paintLook(doc.documentElement, look, props.scheme, props.accent);
  }, [ready, look, props.scheme, props.accent]);

  useEffect(() => {
    const doc = frame.current?.contentDocument;
    if (ready && doc) doc.documentElement.lang = locale;
  }, [ready, locale]);

  useEffect(() => {
    const own = frame.current?.contentDocument?.getElementById("mikan-css");
    if (!ready || !own) return;
    // Setting the text restyles the whole frame, so only a change is written.
    const text = cleanCSS(css);
    if (own.textContent !== text) own.textContent = text;
  }, [ready, css]);

  // The page itself follows the editor a beat behind: typing stays ahead of the frame.
  const shown = useDeferredValue(props);
  useEffect(() => {
    if (ready) root.current?.render(<PreviewPage {...shown} />);
  }, [ready, shown, locale]);

  return (
    <div ref={stage} className="relative w-full overflow-hidden" style={{ height: scale ? h * scale + 2 : h }}>
      <iframe
        ref={frame}
        title={t("settings.page.previewFrame")}
        srcDoc={FRAME_HTML}
        onLoad={init}
        className="absolute top-0 origin-top-left border border-[var(--hairline)] bg-[var(--bg)] shadow-[var(--shadow-md)]"
        style={{
          width: size.w,
          height: h,
          left: scale ? Math.max(0, (width - size.w * scale) / 2) : 0,
          transform: `scale(${scale || 1})`,
          borderRadius: props.device === "phone" ? 36 : 12,
          visibility: scale ? "visible" : "hidden",
        }}
      />
    </div>
  );
}

/** The page inside the frame: the same blocks the subscriber gets, with a sample's data. */
function PreviewPage(p: PreviewProps) {
  const [docId, setDocId] = useState<number | null>(null);
  const docs: DocItem[] = p.docs;
  // A preview goes nowhere: its links and buttons stay on the page.
  const stay = (e: MouseEvent) => {
    if ((e.target as Element).closest?.("a")) e.preventDefault();
  };
  const shell = (children: React.ReactNode) => (
    <div onClickCapture={stay}>
      <PageBackdrop look={p.config.look} image={p.background} />
      <PageShell config={p.config} brand={p.brand} logo={p.logo} right={<span className="ml-auto text-xs font-semibold text-[var(--ink-500)]">{getLocale().toUpperCase()}</span>}>
        {children}
      </PageShell>
    </div>
  );
  if (docId !== null) {
    const doc = p.docs.find((d) => d.id === docId);
    return shell(<DocScreen key={docId} item={doc} load={() => (doc ? Promise.resolve(doc) : Promise.reject(new Error("gone")))} onBack={() => setDocId(null)} tgMode={p.tg} />);
  }
  const promoTitle = titleOf(p.config, "promo");
  return shell(
    <PageBlocks
      config={p.config}
      info={p.info}
      subURL={p.subURL}
      tgMode={p.tg}
      docs={docs}
      platform={p.platform}
      selling={p.tg}
      promo={
        <section className="glass sub-card p-4" inert>
          <h2 className="text-[15px] font-semibold">{promoTitle || t("sub.promoTitle")}</h2>
          <p className="mt-1 text-xs text-[var(--ink-500)]">{t("sub.promoHint")}</p>
          <div className="mt-3 flex gap-2">
            <input className="input min-w-0 flex-1" placeholder="WELCOME30" readOnly />
            <Button variant="primary">{t("sub.promoApply")}</Button>
          </div>
        </section>
      }
      shop={
        <div inert>
          <Shop
            data={p.shop}
            offers={p.shop.offers}
            subRoot=""
            initData=""
            token=""
            title={titleOf(p.config, "shop") || t("sub.shop")}
            openInvoice={() => undefined}
            openLink={() => undefined}
            onRefresh={() => undefined}
          />
        </div>
      }
      reissue={
        <div inert>
          <ReissueSection info={{ ...p.info, reissue: true }} subURL="" title={titleOf(p.config, "reissue")} onDone={() => undefined} />
        </div>
      }
      reload={() => Promise.resolve()}
      onOpenDoc={setDocId}
    />,
  );
}
