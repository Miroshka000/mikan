// The subscription page's body as the admin set it up (page.ts): its header, backdrop and
// blocks in their order. The page (main.tsx) gives it the subscriber's data; the admin's
// preview gives it a sample, so both show the same thing.
import { Check, ChevronLeft, ChevronRight, Copy, FileText, LifeBuoy, MapPin, QrCode, Send } from "lucide-react";
import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import { Atmosphere } from "../components/atmosphere";
import { Bar, Button, Pill, QR, Ring, Skeleton } from "../components/ui";
import { t } from "../i18n";
import { bytes, dateLong, dateShort, days, daysUntil } from "../lib/format";
import { Markdown } from "../lib/markdown";
import { safeHref } from "../lib/url";
import { APPS, detect, enc, type Platform } from "./apps";
import { Devices } from "./devices";
import { outside } from "./net";
import { backdropStyle, blockTitle, MINI_APP_ONLY, orderApps, PLATFORMS, type DocItem, type DocView, type PageBlock, type PageConfig, type PageLook } from "./page";
import type { Info } from "./types";

const PLATFORM_LABEL: Record<Platform, string> = { ios: "iPhone", android: "Android", windows: "Windows", macos: "Mac", linux: "Linux" };

/** What is behind the cards: the theme's dawn, or the admin's colour, gradient or picture. */
export function PageBackdrop({ look, image }: { look: PageLook; image?: string }) {
  const style = backdropStyle(look, image);
  if (!style) return <Atmosphere calm />;
  const dim = look.background.kind === "image" ? look.background.dim : 0;
  return (
    <div className="sub-backdrop" style={style} aria-hidden>
      {dim > 0 ? <i style={{ opacity: dim / 100 }} /> : null}
    </div>
  );
}

/** The column the page lives in, with the brand on top. */
export function PageShell({ config, brand, logo, right, children }: { config: PageConfig; brand: string; logo?: string; right?: ReactNode; children: ReactNode }) {
  const b = config.brand;
  const mark =
    b.logo === "image" && logo ? (
      <img src={logo} alt="" width={28} height={28} className="h-7 w-7 shrink-0 rounded-[9px] object-cover" decoding="async" />
    ) : b.logo === "emoji" && b.emoji ? (
      <span className="grid h-7 w-7 shrink-0 place-items-center text-[20px] leading-none" aria-hidden>
        {b.emoji}
      </span>
    ) : b.logo === "none" ? null : (
      <span className="font-display grid h-7 w-7 shrink-0 place-items-center rounded-[9px] bg-[var(--fill)] text-[13px] font-semibold text-[var(--on-fill)]" aria-hidden>
        {(brand || "V")[0]}
      </span>
    );
  return (
    <main className="calm-glass mx-auto flex max-w-[440px] flex-col gap-3 px-4 pt-[calc(24px+env(safe-area-inset-top))] pb-[calc(40px+env(safe-area-inset-bottom))]">
      <div className="flex items-center gap-2 px-1 pb-1">
        {mark}
        {b.subtitle ? (
          <span className="flex min-w-0 flex-col">
            <span className="font-display truncate text-[15px] leading-5 font-semibold tracking-tight">{brand}</span>
            <span className="truncate text-xs text-[var(--ink-500)]">{b.subtitle}</span>
          </span>
        ) : (
          <span className="font-display text-[15px] font-semibold tracking-tight">{brand}</span>
        )}
        {right}
      </div>
      {children}
    </main>
  );
}

export type BlocksProps = {
  config: PageConfig;
  info: Info;
  subURL: string;
  /** The page runs as the bot's Mini App: the shop and promo codes show, links leave through Telegram. */
  tgMode: boolean;
  docs: DocItem[];
  /** The Mini App's own sections, built by the page: promo codes, the shop and the packages. */
  promo?: ReactNode;
  shop?: ReactNode;
  /** The shop sells here: "renew" goes to it instead of support. */
  selling?: boolean;
  reload: () => Promise<void>;
  onOpenDoc: (id: number) => void;
  /** The platform tab open first; the admin's choice (or the device) when not given. */
  platform?: Platform;
};

/** The page's blocks in the admin's order, the ones turned off left out. */
export function PageBlocks(p: BlocksProps) {
  return (
    <>
      {p.config.blocks.map((b) => (b.on && (p.tgMode || !MINI_APP_ONLY.includes(b.type)) ? <Fragment key={b.id}>{block(b, p)}</Fragment> : null))}
    </>
  );
}

function block(b: PageBlock, p: BlocksProps): ReactNode {
  const { info } = p;
  switch (b.type) {
    case "announce":
      return info.announce ? <Announce block={b} text={info.announce} url={info.announce_url} /> : null;
    case "status":
      return <Status info={info} selling={!!p.selling} />;
    case "promo":
      return p.promo ?? null;
    case "shop":
      return p.shop ?? null;
    case "traffic":
      return <Traffic block={b} info={info} />;
    case "devices":
      return info.binding || info.devices?.length ? <Devices info={info} subURL={p.subURL} reload={p.reload} title={b.title} /> : null;
    case "apps":
      return <Apps key={p.platform} block={b} config={p.config} subURL={p.subURL} brand={info.brand} happ={info.happ_link} tgMode={p.tgMode} platform={p.platform} />;
    case "guide":
      return <Guide block={b} />;
    case "instructions":
      return p.docs.length ? <Instructions block={b} docs={p.docs} onOpen={p.onOpenDoc} /> : null;
    case "link":
      return <SubLink block={b} subURL={p.subURL} qr={p.config.apps.qr} />;
    case "locations":
      return info.locations?.length ? <Locations block={b} names={info.locations} /> : null;
    case "telegram": {
      const telegram = safeHref(info.telegram);
      return telegram && !p.tgMode ? (
        <a className="btn btn-glass btn-block h-12 rounded-2xl" href={telegram} target="_blank" rel="noreferrer noopener">
          <Send size={18} aria-hidden /> {blockTitle(b, t("sub.openTelegram"))}
        </a>
      ) : null;
    }
    case "support": {
      const support = safeHref(info.support_url, { tg: true });
      return support ? (
        <a className="btn btn-glass btn-block h-12 rounded-2xl" href={support} target="_blank" rel="noreferrer noopener" {...outside(support)}>
          <LifeBuoy size={18} aria-hidden /> {blockTitle(b, t("sub.support"))}
        </a>
      ) : null;
    }
    case "text":
      return b.text ? <TextBlock block={b} tgMode={p.tgMode} /> : null;
    case "links":
      return b.links?.length ? <Links block={b} /> : null;
  }
  return null;
}

/** A block's heading: the admin's own, or its usual one. */
function Heading({ children, className = "mb-3" }: { children: ReactNode; className?: string }) {
  return <h2 className={`${className} text-[15px] font-semibold`}>{children}</h2>;
}

function Status({ info, selling }: { info: Info; selling: boolean }) {
  const d = info.expires_at ? daysUntil(info.expires_at) : null;
  const firstName = info.name.split(/\s+/)[0] ?? "";
  const tone = ({ active: "ok", expiring: "warn", limited: "bad", expired: "bad", disabled: "off" } as const)[info.state];
  // Links from the server are followed only when they are http(s) (or Telegram's own).
  const support = safeHref(info.support_url, { tg: true });
  return (
    <section className="glass sub-card reveal p-4">
      <h1 className="font-display text-xl leading-7 font-medium tracking-tight">{t(`sub.status.${info.state}`, { name: firstName })}</h1>
      <div className="mt-2 flex items-center justify-between gap-2 text-[13px] text-[var(--ink-600)]">
        <span>{info.expires_at ? t("sub.until", { date: dateLong(info.expires_at) }) : t("sub.forever")}</span>
        {d !== null && d >= 0 ? <Pill tone={tone}>{days(d)}</Pill> : null}
      </div>
      {(info.state === "expired" || info.state === "limited" || info.state === "disabled") && support && !selling ? (
        <a className="btn btn-primary btn-block mt-4" href={support} target="_blank" rel="noreferrer noopener" {...outside(support)}>
          {t("sub.renew")}
        </a>
      ) : null}
    </section>
  );
}

function Traffic({ block: b, info }: { block: PageBlock; info: Info }) {
  const used = info.used_up + info.used_down;
  const extra = info.extra ?? 0;
  // Traffic packages are spent after the tariff's traffic: they add to what is left.
  const left = info.limit != null ? Math.max(0, info.limit - used) + extra : null;
  const cap = info.limit != null ? Math.max(info.limit, used) + extra : 0;
  const pct = cap ? (used / cap) * 100 : 0;
  const ofTotal = (limit: number, more: number) => t("sub.of", { total: more > 0 ? t("sub.plusPackages", { limit: bytes(limit), extra: bytes(more) }) : bytes(limit) });
  const [leftValue, leftUnit] = left != null ? bytes(left).split(" ") : ["∞", ""];
  const numbers = (
    <>
      <Ring size={104} pct={info.limit != null ? pct : 100} label={leftValue} sub={left != null ? t("sub.left", { unit: leftUnit ?? "" }) : t("sub.unlimited")} />
      <div className="flex flex-col gap-2 text-xs text-[var(--ink-500)]">
        <div>
          {t("sub.used")}
          <b className="num block text-base font-medium text-[var(--ink-900)]">{info.limit != null ? `${bytes(used)} ${ofTotal(info.limit, extra)}` : bytes(used)}</b>
        </div>
        {info.resets_at ? (
          <div>
            {t("sub.resets")}
            <b className="block text-base font-medium text-[var(--ink-900)]">{dateShort(info.resets_at)}</b>
          </div>
        ) : null}
        {info.device_limit ? (
          <div>
            {t("sub.devices")}
            <b className="num block text-base font-medium text-[var(--ink-900)]">{t("sub.upTo", { n: info.device_limit })}</b>
          </div>
        ) : null}
        {info.speed_limit ? (
          <div>
            {t("sub.speed")}
            <b className="num block text-base font-medium text-[var(--ink-900)]">{t("sub.speedUpTo", { n: info.speed_limit })}</b>
          </div>
        ) : null}
      </div>
    </>
  );
  return (
    <>
      {b.title ? (
        <section className="glass sub-card p-4">
          <Heading>{b.title}</Heading>
          <div className="grid grid-cols-[104px_1fr] items-center gap-4">{numbers}</div>
        </section>
      ) : (
        <section className="glass sub-card grid grid-cols-[104px_1fr] items-center gap-4 p-4">{numbers}</section>
      )}
      {info.pools?.length ? (
        <section className="glass sub-card p-4">
          <Heading>{t("sub.pools")}</Heading>
          <ul className="flex flex-col gap-3">
            {info.pools.map((pool) => (
              <li key={pool.name}>
                <div className="mb-1 flex items-center justify-between gap-2 text-[13px]">
                  <span className="font-medium">{pool.name}</span>
                  <span className="num text-xs text-[var(--ink-600)]">{pool.limit != null ? `${bytes(pool.used)} ${ofTotal(pool.limit, pool.extra ?? 0)}` : bytes(pool.used)}</span>
                </div>
                {pool.limit != null ? <Bar label={pool.name} pct={Math.min(100, (pool.used / (Math.max(pool.limit, pool.used) + (pool.extra ?? 0))) * 100)} /> : null}
                {pool.limit != null && pool.used >= pool.limit && !pool.extra ? <p className="mt-1 text-xs text-[var(--berry-600)]">{t("sub.poolOut")}</p> : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  );
}

/** The tab a page opens on: the admin's, or the visitor's device. */
export function firstPlatform(config: PageConfig, detected: Platform): Platform {
  return config.apps.platform === "auto" ? detected : config.apps.platform;
}

function Apps({ block: b, config, subURL, brand, happ, tgMode, platform }: { block: PageBlock; config: PageConfig; subURL: string; brand: string; happ?: string; tgMode: boolean; platform?: Platform }) {
  const lists = Object.fromEntries(PLATFORMS.map((k) => [k, orderApps(APPS[k], config.apps[k])])) as Record<Platform, (typeof APPS)[Platform]>;
  const shown = PLATFORMS.filter((k) => lists[k].length > 0);
  const want = platform ?? firstPlatform(config, detectOnce());
  const [current, setCurrent] = useState<Platform>(shown.includes(want) ? want : (shown[0] ?? want));
  if (shown.length === 0) return null;
  const tab = shown.includes(current) ? current : shown[0]!;
  return (
    <section className="glass sub-card p-4">
      <Heading>{blockTitle(b, t("sub.connect"))}</Heading>
      {shown.length > 1 ? (
        <div className="mb-3 flex gap-1 overflow-x-auto rounded-[14px] bg-[var(--hover)] p-1" role="group" aria-label={t("sub.platform")}>
          {shown.map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={tab === k}
              onClick={() => setCurrent(k)}
              className="h-8 flex-auto shrink-0 rounded-[10px] px-1.5 text-xs font-semibold whitespace-nowrap text-[var(--ink-600)] aria-pressed:bg-[var(--surface-solid)] aria-pressed:text-[var(--ink-900)] aria-pressed:shadow-sm"
            >
              {PLATFORM_LABEL[k]}
            </button>
          ))}
        </div>
      ) : null}
      <div className="row-list">
        {lists[tab].map((a, i) => (
          <div key={a.name} className="grid grid-cols-[40px_minmax(0,1fr)_auto] items-center gap-3 py-2">
            <span className="font-display grid h-10 w-10 place-items-center rounded-xl border border-[var(--hairline)] bg-[var(--surface-solid)] text-sm font-semibold text-[var(--ink-700)]" aria-hidden>
              {a.name[0]}
            </span>
            <div className="min-w-0">
              <div className="text-sm font-semibold">{a.name}</div>
              <div className="text-xs text-[var(--ink-500)]">
                {i === 0 ? <span className="font-medium text-[var(--mikan-700)]">{t("sub.recommended")} · </span> : null}
                {t(`sub.notes.${a.note}`)}
              </div>
            </div>
            <a
              className={i === 0 ? "btn btn-primary btn-sm" : "btn btn-glass btn-sm"}
              href={a.link(subURL, brand, happ)}
              title={tgMode ? t("sub.tgBrowser") : undefined}
              {...outside(subURL + "#open=" + enc(a.name))}
            >
              {t("common.add")}
            </a>
          </div>
        ))}
      </div>
    </section>
  );
}

let detected: Platform | undefined;
// The device does not change while the page is open.
const detectOnce = (): Platform => (detected ??= detect());

function Guide({ block: b }: { block: PageBlock }) {
  return (
    <section className="glass sub-card p-4">
      <Heading>{blockTitle(b, t("sub.howTo"))}</Heading>
      <ol className="flex flex-col gap-3 text-[13px] text-[var(--ink-600)]">
        {([1, 2, 3] as const).map((n) => (
          <li key={n} className="grid grid-cols-[28px_1fr] items-start gap-3">
            <span className="font-display grid h-7 w-7 place-items-center rounded-full border border-[var(--hairline)] bg-[var(--surface-solid)] text-xs font-semibold">{n}</span>
            <div>
              <b className="block font-semibold text-[var(--ink-900)]">{t(`sub.steps.${n}.title`)}</b>
              {t(`sub.steps.${n}.text`)}
            </div>
          </li>
        ))}
      </ol>
    </section>
  );
}

function Instructions({ block: b, docs, onOpen }: { block: PageBlock; docs: DocItem[]; onOpen: (id: number) => void }) {
  return (
    <section className="glass sub-card p-4">
      <Heading className="mb-1">{blockTitle(b, t("sub.docs"))}</Heading>
      <ul className="row-list">
        {docs.map((d) => (
          <li key={d.id}>
            <button type="button" id={`doc-${d.id}`} className="grid w-full grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-3 py-3 text-left" onClick={() => onOpen(d.id)}>
              <span className="grid h-8 w-8 place-items-center rounded-[10px] bg-[var(--hover)] text-base leading-none text-[var(--ink-600)]" aria-hidden>
                {d.emoji || <FileText size={16} />}
              </span>
              <span className="min-w-0">
                <span className="block truncate text-sm font-semibold">{d.title}</span>
                {d.platform ? <span className="block text-xs text-[var(--ink-500)]">{PLATFORM_LABEL[d.platform as Platform] ?? d.platform}</span> : null}
              </span>
              <ChevronRight size={18} className="text-[var(--ink-400)]" aria-hidden />
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function SubLink({ block: b, subURL, qr }: { block: PageBlock; subURL: string; qr: boolean }) {
  const [showQR, setShowQR] = useState(false);
  const [copied, setCopied] = useState(false);
  const field = useRef<HTMLSpanElement>(null);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(subURL);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      // No clipboard here: the code, or the link selected for the system's own copy.
      if (qr) setShowQR(true);
      else if (field.current) window.getSelection()?.selectAllChildren(field.current);
    }
  };
  return (
    <section className="glass sub-card p-4">
      <Heading>{blockTitle(b, t("sub.link"))}</Heading>
      <div className="link-field">
        <span ref={field} className="mono">
          {subURL}
        </span>
        <button type="button" className="icon-btn" onClick={copy} aria-label={t("common.copyLink")}>
          {copied ? <Check size={18} className="text-[var(--leaf-500)]" /> : <Copy size={18} />}
        </button>
      </div>
      {qr ? (
        <>
          <button type="button" className="btn btn-glass btn-sm mt-2" onClick={() => setShowQR((v) => !v)} aria-expanded={showQR}>
            <QrCode size={16} aria-hidden /> {t("sub.qrOther")}
          </button>
          {showQR ? (
            <div className="mt-3 flex justify-center">
              <QR value={subURL} size={200} />
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function Announce({ block: b, text, url }: { block: PageBlock; text: string; url?: string }) {
  const href = safeHref(url, { tg: true });
  return (
    <section className="glass sub-card p-4" role="note">
      {b.title ? <Heading className="mb-1">{b.title}</Heading> : null}
      <p className="text-[13px] leading-5 text-[var(--ink-700)]">{text}</p>
      {href ? (
        <a className="btn btn-glass btn-sm mt-3" href={href} target="_blank" rel="noreferrer noopener" {...outside(href)}>
          {t("sub.announceMore")}
        </a>
      ) : null}
    </section>
  );
}

function Locations({ block: b, names }: { block: PageBlock; names: string[] }) {
  return (
    <section className="glass sub-card p-4">
      <Heading>{blockTitle(b, t("sub.locations"))}</Heading>
      <ul className="flex flex-wrap gap-2">
        {names.map((n) => (
          <li key={n} className="inline-flex h-8 items-center gap-1.5 rounded-full border border-[var(--hairline)] bg-[var(--surface-soft)] px-3 text-[13px] font-medium text-[var(--ink-700)]">
            <MapPin size={14} className="text-[var(--mikan-600)]" aria-hidden />
            {n}
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Links of the Markdown open outside the Mini App (Telegram's bridge), in a new tab elsewhere. */
const mdLink = (tgMode: boolean) => (tgMode ? (href: string) => outside(href) : undefined);

function TextBlock({ block: b, tgMode }: { block: PageBlock; tgMode: boolean }) {
  return (
    <section className="glass sub-card p-4">
      {b.title ? <Heading>{b.title}</Heading> : null}
      <Markdown text={b.text ?? ""} link={mdLink(tgMode)} />
    </section>
  );
}

function Links({ block: b }: { block: PageBlock }) {
  const buttons = (b.links ?? []).map((l, i) => {
    const href = safeHref(l.url, { tg: true });
    return href ? (
      <a key={i} className="btn btn-glass btn-block h-12 rounded-2xl" href={href} target="_blank" rel="noreferrer noopener" {...outside(href)}>
        {l.emoji ? (
          <span className="text-base leading-none" aria-hidden>
            {l.emoji}
          </span>
        ) : null}
        <span className="truncate">{l.label}</span>
      </a>
    ) : null;
  });
  if (!b.title) return <>{buttons}</>;
  return (
    <section className="glass sub-card p-4">
      <Heading>{b.title}</Heading>
      <div className="flex flex-col gap-2">{buttons}</div>
    </section>
  );
}

/** An instruction on a screen of its own, with the way back to the page. */
export function DocScreen({ item, load, onBack, tgMode }: { item?: DocItem; load: () => Promise<DocView>; onBack: () => void; tgMode: boolean }) {
  const [doc, setDoc] = useState<DocView | "loading" | "failed">("loading");
  const [attempt, setAttempt] = useState(0);
  const heading = useRef<HTMLHeadingElement>(null);
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => {
    let live = true;
    setDoc("loading");
    loadRef
      .current()
      .then((d) => live && setDoc(d))
      .catch(() => live && setDoc("failed"));
    return () => {
      live = false;
    };
  }, [attempt]);
  useEffect(() => {
    if (typeof doc === "object") heading.current?.focus();
  }, [doc]);
  const shown = typeof doc === "object" ? doc : item;
  return (
    <>
      <div>
        <Button variant="ghost" size="sm" className="-ml-2" onClick={onBack}>
          <ChevronLeft size={16} aria-hidden /> {t("sub.docsBack")}
        </Button>
      </div>
      <article className="glass sub-card p-4" aria-busy={doc === "loading" || undefined}>
        {shown ? (
          <div className="mb-3 flex items-start gap-3">
            {shown.emoji ? (
              <span className="text-[24px] leading-7" aria-hidden>
                {shown.emoji}
              </span>
            ) : null}
            <div className="min-w-0">
              <h1 ref={heading} tabIndex={-1} className="font-display text-xl leading-7 font-medium tracking-tight outline-none">
                {shown.title}
              </h1>
              {shown.platform ? <div className="mt-1 text-xs text-[var(--ink-500)]">{PLATFORM_LABEL[shown.platform as Platform] ?? shown.platform}</div> : null}
            </div>
          </div>
        ) : null}
        {doc === "loading" ? (
          <div className="flex flex-col gap-2" role="status" aria-label={t("common.loading")}>
            <Skeleton style={{ width: "92%" }} />
            <Skeleton style={{ width: "80%" }} />
            <Skeleton style={{ width: "86%" }} />
            <Skeleton style={{ width: "40%" }} />
          </div>
        ) : doc === "failed" ? (
          <div role="alert">
            <p className="text-[13px] text-[var(--ink-600)]">{t("sub.docsFailed")}</p>
            <Button variant="primary" size="sm" className="mt-3" onClick={() => setAttempt((n) => n + 1)}>
              {t("common.retry")}
            </Button>
          </div>
        ) : doc.body ? (
          <Markdown text={doc.body} link={mdLink(tgMode)} />
        ) : (
          <p className="text-[13px] text-[var(--ink-500)]">{t("sub.docsEmpty")}</p>
        )}
      </article>
    </>
  );
}
