// The Mini App's shop: the plans on sale and a way to pay for them. Stars open Telegram's
// own payment sheet; the marketplace's adapters (YooKassa, CryptoBot…) open their pages. The subscription itself is
// issued or renewed by the panel once the provider confirms the payment.
import { Check, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "../components/ui";
import { t, type Key } from "../i18n";
import { money, rubles } from "../lib/format";
import { okData, postJSON } from "./net";

/** A term of a plan sold for several: its own price; label is "30 days", "3 months". */
type Term = { days: number; label: string; description: string; stars?: number; rub?: number };
type Offer = { id: number; name: string; description: string; note?: string; stars?: number; rub?: number; terms?: Term[] };
export type ShopData = {
  allow_new: boolean;
  providers: { stars: boolean };
  /** Marketplace adapters that take rubles, by the name the buyer sees. */
  addons?: { provider: `addon:${string}`; name: string }[];
  offers: Offer[];
  packages?: Offer[];
  /** The priced trial the account may buy now: once, before any plan. */
  trial?: Offer;
  /** The free plan the account may take now; channel is the one it asks the account to join. */
  free?: { name: string; description: string; channel?: string };
};
type Provider = "stars" | `addon:${string}`;

// YooKassa and CryptoBot keep the words buyers knew them by, and come first.
const KNOWN: Record<string, Key> = { "addon:yookassa": "sub.shopCard", "addon:cryptobot": "sub.shopCrypto" };
const rank = (p: string) => (p === "addon:yookassa" ? 0 : p === "addon:cryptobot" ? 1 : 2);

const FAIL: Record<string, Key> = {
  not_for_sale: "sub.shopNotForSale",
  too_many_invoices: "sub.shopTooMany",
  too_many_subs: "sub.shopTooManySubs",
  promo_unavailable: "sub.promoUnavailable",
  promo_try_later: "sub.promoTryLater",
  trial_used: "sub.trialUsed",
  trial_off: "sub.trialOff",
};

/** What the account can buy; with token, the traffic packages of that subscription too. */
export function loadShop(subRoot: string, initData: string, token = ""): Promise<ShopData> {
  return postJSON<ShopData>(subRoot + "/tg/shop", { init_data: initData, token }).then(okData);
}

/**
 * Offers and pay buttons: plans (field tariff_id), traffic packages (package_id) or the
 * priced trial (trial). token is the subscription to renew or add traffic to; "" buys a
 * new one. openInvoice/openLink go through the Mini App bridge.
 */
export function Shop({
  data,
  offers,
  field = "tariff_id",
  pick = t("sub.shopPick"),
  subRoot,
  initData,
  token,
  title,
  openInvoice,
  openLink,
  onRefresh,
  promoCode = "",
}: {
  data: ShopData;
  offers: Offer[];
  field?: "tariff_id" | "package_id" | "trial";
  pick?: string;
  subRoot: string;
  initData: string;
  token: string;
  title: string;
  openInvoice: (slug: string) => void;
  openLink: (url: string) => void;
  onRefresh: () => void;
  promoCode?: string;
}) {
  const [picked, setPicked] = useState<number | null>(offers.length === 1 ? offers[0]!.id : null);
  // The chosen term of a plan sold for several; the first until another is chosen.
  const [termDays, setTermDays] = useState<number | null>(null);
  const [busy, setBusy] = useState<Provider | null>(null);
  const [error, setError] = useState("");
  const [promoPreview, setPromoPreview] = useState<{ discount: number; final_amount: number; currency: string } | null>(null);
  const [opened, setOpened] = useState(false);
  // The payment page of a ruble provider, ready to open. Telegram opens a link only for a tap
  // it sees, and the requests that made this link took that tap, so the buyer taps again.
  const [ready, setReady] = useState<{ url: string; provider: Provider } | null>(null);
  useEffect(() => { setError(""); setPromoPreview(null); setReady(null); }, [picked, termDays, promoCode]);
  useEffect(() => setTermDays(null), [picked]);
  const offer = offers.find((o) => o.id === picked);
  const terms = offer?.terms && offer.terms.length > 1 ? offer.terms : null;
  const term = terms ? (terms.find((x) => x.days === termDays) ?? terms[0]!) : null;
  // What is paid for: the chosen term's prices, or the offer's own.
  const price = term ?? offer;
  // A single term the providers take now is named too: it may not be the plan's first,
  // which a request without days is for.
  const sold = term ?? offer?.terms?.[0];
  const order = field === "trial" ? { trial: true } : { [field]: offer?.id, ...(sold ? { term_days: sold.days } : {}) };
  const failText = (code: string) => (field === "package_id" && code === "not_for_sale" ? t("sub.packageNotForSale") : t(FAIL[code] ?? "sub.shopFail"));

  const pay = async (provider: Provider) => {
    if (!offer) return;
    setBusy(provider);
    setError("");
    setReady(null);
    try {
      if (promoCode) {
        const preview = await postJSON<{ code: string; discount: number; final_amount: number; currency: string }>(subRoot + "/tg/promo", {
          init_data: initData,
          token,
          code: promoCode,
          ...order,
          provider,
          validate_only: true,
        });
        const result = preview.data;
        if (!preview.ok) {
          setError(failText(result.code ?? "promo_unavailable"));
          return;
        }
        if (result.discount != null && result.final_amount != null && result.currency) {
          setPromoPreview({ discount: result.discount, final_amount: result.final_amount, currency: result.currency });
        }
      }
      const r = await postJSON<{ url: string; code: string }>(subRoot + "/tg/pay", { init_data: initData, ...order, provider, token, promo_code: promoCode });
      const body = r.data;
      if (!r.ok || !body.url) {
        setError(failText(body.code ?? ""));
        return;
      }
      // A Stars link is https://t.me/$<slug>: Telegram opens its payment sheet for it.
      const slug = /^https:\/\/t\.me\/\$(.+)$/.exec(body.url)?.[1];
      if (provider === "stars" && slug) {
        openInvoice(slug);
        setOpened(true);
      } else setReady({ url: body.url, provider });
    } catch {
      setError(t("sub.shopFail"));
    } finally {
      setBusy(null);
    }
  };

  const methods: { id: Provider; label: string; price?: string }[] = [];
  if (price?.stars && data.providers.stars) methods.push({ id: "stars", label: t("sub.shopStars"), price: `⭐ ${price.stars}` });
  if (price?.rub) {
    const adapters = [...(data.addons ?? [])].sort((a, b) => rank(a.provider) - rank(b.provider) || a.provider.localeCompare(b.provider));
    for (const a of adapters) methods.push({ id: a.provider, label: KNOWN[a.provider] ? t(KNOWN[a.provider]!) : a.name, price: rubles(price.rub) });
  }

  const goName = ready ? data.addons?.find((a) => a.provider === ready.provider)?.name : undefined;

  return (
    <section className="glass sub-card p-4" aria-label={title}>
      <h2 className="mb-1 text-[15px] font-semibold">{title}</h2>
      <p className="mb-3 text-xs text-[var(--ink-500)]">{pick}{promoCode ? ` · ${t("sub.promoSelected", { code: promoCode })}` : ""}</p>
      {promoPreview ? <p className="mb-3 text-xs text-[var(--leaf-600)]">{t("sub.promoCheckoutDiscount", { discount: money(promoPreview.discount, promoPreview.currency), total: money(promoPreview.final_amount, promoPreview.currency) })}</p> : null}
      <div className="flex flex-col gap-2" role="radiogroup" aria-label={pick}>
        {offers.map((o) => (
          <button key={o.id} type="button" role="radio" aria-checked={picked === o.id} className="opt" onClick={() => setPicked(o.id)}>
            <span className="flex items-center justify-between gap-2">
              <span className="font-semibold">{o.name}</span>
              <span className="num text-[13px] text-[var(--ink-700)]">{listPrice(o)}</span>
            </span>
            <span className="text-xs text-[var(--ink-500)]">{o.description}</span>
            {o.note ? <span className="whitespace-pre-line text-xs text-[var(--ink-700)]">{o.note}</span> : null}
          </button>
        ))}
      </div>
      {terms ? (
        <div className="mt-3 flex flex-wrap gap-2" role="radiogroup" aria-label={t("sub.shopPickTerm")}>
          {terms.map((x) => (
            <button key={x.days} type="button" role="radio" aria-checked={term?.days === x.days} className="opt flex-1" style={{ minWidth: 96 }} onClick={() => setTermDays(x.days)}>
              <span className="font-semibold">{x.label}</span>
              <span className="num text-xs text-[var(--ink-600)]">{x.rub ? rubles(x.rub) : `⭐ ${x.stars}`}</span>
            </button>
          ))}
        </div>
      ) : null}
      {offer ? (
        <div className="mt-3 grid gap-2" style={{ gridTemplateColumns: `repeat(${Math.min(3, Math.max(1, methods.length))}, minmax(0, 1fr))` }}>
          {methods.map((m, i) => (
            <Button key={m.id} variant={i === 0 ? "primary" : "glass"} className="flex-col gap-0 py-2" style={{ height: "auto" }} loading={busy === m.id} disabled={!!busy} onClick={() => void pay(m.id)}>
              <span className="text-[13px] font-semibold">{m.price}</span>
              <span className="text-[11px] opacity-80">{m.label}</span>
            </Button>
          ))}
        </div>
      ) : null}
      {ready && !busy && !error ? (
        <div className="mt-3 flex flex-col gap-2">
          <Button
            variant="primary"
            block
            onClick={() => {
              // Straight from the tap: no await between it and the bridge call.
              openLink(ready.url);
              setOpened(true);
            }}
          >
            {goName ? t("sub.shopGoVia", { name: goName }) : t("sub.shopGo")}
          </Button>
          {opened ? null : <p className="text-xs text-[var(--ink-500)]">{t("sub.shopGoHint")}</p>}
        </div>
      ) : null}
      {error ? (
        <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
          {error}
        </p>
      ) : null}
      {opened && !error ? (
        <div className="mt-3 flex items-center justify-between gap-2 text-xs text-[var(--ink-600)]" role="status">
          <span className="flex items-center gap-2">
            <Check size={14} className="shrink-0 text-[var(--leaf-500)]" aria-hidden /> {t("sub.shopDone")}
          </span>
          <Button size="sm" variant="ghost" onClick={onRefresh}>
            <RefreshCw size={14} aria-hidden /> {t("sub.shopRefresh")}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

const FREE_FAIL: Record<string, Key> = {
  free_active: "sub.freeActive",
  free_off: "sub.freeOff",
  too_many_subs: "sub.shopTooManySubs",
};

/**
 * The free plan: one tap gives the subscription. A plan for the channel's subscribers
 * shows the channel next to it, and says so when the account is not in it yet.
 */
export function FreePlan({
  offer,
  subRoot,
  initData,
  openLink,
  onTaken,
}: {
  offer: NonNullable<ShopData["free"]>;
  subRoot: string;
  initData: string;
  openLink: (url: string) => void;
  onTaken: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const take = async () => {
    setBusy(true);
    setError("");
    try {
      const r = await postJSON<{ token: string; code: string }>(subRoot + "/tg/free", { init_data: initData });
      if (r.ok && r.data.token) {
        onTaken();
        return;
      }
      const code = r.data.code ?? "";
      setError(code === "free_channel" ? t("sub.freeChannelNeeded") : t(FREE_FAIL[code] ?? "sub.freeFail"));
    } catch {
      setError(t("sub.freeFail"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="glass sub-card p-4" aria-label={t("sub.freeTitle")}>
      <h2 className="mb-1 text-[15px] font-semibold">{t("sub.freeTitle")}</h2>
      <p className="mb-3 text-xs text-[var(--ink-500)]">{t("sub.freeAgain")}</p>
      <div className="mb-3">
        <div className="font-semibold">{offer.name}</div>
        <div className="text-xs text-[var(--ink-500)]">{offer.description}</div>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" loading={busy} disabled={busy} onClick={() => void take()}>
          {t("sub.freeTake")}
        </Button>
        {offer.channel ? (
          <Button variant="glass" onClick={() => openLink(offer.channel!)}>
            {t("sub.freeJoin")}
          </Button>
        ) : null}
      </div>
      {error ? (
        <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
          {error}
        </p>
      ) : null}
    </section>
  );
}

/** The price a list shows: rubles when sold for them, else Stars; "from" the cheapest term
 * of a plan sold for several. */
function listPrice(o: Offer): string {
  const terms = o.terms && o.terms.length > 1 ? o.terms : [o];
  const rub = Math.min(...terms.map((x) => x.rub || Infinity));
  const price = Number.isFinite(rub) ? rubles(rub) : `⭐ ${Math.min(...terms.map((x) => x.stars || Infinity))}`;
  return terms.length > 1 ? t("sub.shopFrom", { price }) : price;
}
