import { useEffect, useEffectEvent, useState } from "react";
import { Button, Skeleton } from "../components/ui";
import { t, useLocale } from "../i18n";
import { safeHref } from "../lib/url";
import { checkoutRoot, postJSON } from "./net";
import { Shop, type BrowserOrder, type ShopData } from "./shop";

type Checkout = { key: string; subToken: string; order?: BrowserOrder };
type Result = { status: string; token?: string; url?: string; code?: string; retryable?: boolean };
const storageKey = "mikan-checkout:" + checkoutRoot;

function remember(checkout: Checkout | null) {
  if (checkout) localStorage.setItem(storageKey, JSON.stringify(checkout));
  else localStorage.removeItem(storageKey);
}

function savedCheckout(): Checkout | null {
  try {
    const value: Checkout | null = JSON.parse(localStorage.getItem(storageKey) ?? "null");
    return value && /^[a-f0-9]{64}$/.test(value.key) && typeof value.subToken === "string" ? value : null;
  } catch {
    return null;
  }
}

function newKey() {
  return Array.from(crypto.getRandomValues(new Uint8Array(32)), (b) => b.toString(16).padStart(2, "0")).join("");
}

function openInvoice(checkout: Checkout, email?: string) {
  return postJSON<Result>(checkoutRoot + "/pay", {
    ...checkout.order, email, token: checkout.subToken, checkout_key: checkout.key,
  });
}

export function BrowserShop({ subToken = "", title, onRenewed }: { subToken?: string; title: string; onRenewed?: () => void }) {
  useLocale();
  const [checkout, setCheckout] = useState(() => {
    const saved = savedCheckout();
    return !subToken || saved?.subToken === subToken ? saved : null;
  });
  const [result, setResult] = useState<Partial<Result>>({});
  const [data, setData] = useState<ShopData | null>();
  const [pollFailed, setPollFailed] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [retryFailed, setRetryFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const ctl = new AbortController();
    postJSON<ShopData>(checkoutRoot + "/offers", { token: subToken }, ctl.signal)
      .then((response) => {
        if (!ctl.signal.aborted) setData(response.ok ? response.data as ShopData : null);
      })
      .catch(() => { if (!ctl.signal.aborted) setData(null); });
    return () => ctl.abort();
  }, [subToken, attempt]);

  useEffect(() => {
    if (!checkout || retrying) return;
    const ctl = new AbortController();
    let timer: number | undefined;
    let missing = 0;
    const poll = async () => {
      try {
        const r = await postJSON<Result>(checkoutRoot + "/status", { checkout_key: checkout.key }, ctl.signal);
        if (ctl.signal.aborted) return;
        if (r.status === 404 && ++missing >= 3) {
          setResult({ status: "missing" });
          return;
        }
        if (r.ok) {
          setPollFailed(false);
          setResult(r.data);
          if (r.data.status !== "pending" && r.data.status !== "paid") return;
        } else if (r.status !== 404) setPollFailed(true);
      } catch {
        if (ctl.signal.aborted) return;
        setPollFailed(true);
      }
      timer = window.setTimeout(poll, 4_000);
    };
    void poll();
    return () => { ctl.abort(); window.clearTimeout(timer); };
  }, [checkout, attempt, retrying]);

  const finished = result.status === "applied";
  const refresh = useEffectEvent(() => onRenewed?.());
  useEffect(() => {
    if (finished && checkout?.subToken === subToken) refresh();
  }, [finished, checkout, subToken]);

  const clear = () => {
    remember(null);
    setCheckout(null);
    setResult({});
    setPollFailed(false);
    setRetryFailed(false);
  };

  if (checkout) {
    const href = result.url && safeHref(result.url);
    const token = result.token || checkout.subToken;
    const sub = token ? checkoutRoot.slice(0, -"/shop".length) + "/" + encodeURIComponent(token) : "";
    const failed = ["expired", "failed", "refunded", "missing"].includes(result.status ?? "");
    const pending = !finished && !failed;
    const canRetry = result.retryable && checkout.order;
    const retry = async () => {
      setRetrying(true);
      setRetryFailed(false);
      try {
        const r = await openInvoice(checkout);
        if (r.ok) setResult(r.data);
        else setRetryFailed(true);
      } catch {
        setRetryFailed(true);
      } finally {
        setRetrying(false);
      }
    };
    return (
      <section className="glass sub-card flex flex-col gap-3 p-4" aria-label={title}>
        <h2 className="text-[15px] font-semibold">{title}</h2>
        <p className="text-sm text-[var(--ink-600)]" role="status">
          {t(finished ? "sub.webApplied" : canRetry ? "sub.webInvoiceMissing" : failed ? "sub.webEnded" : pollFailed ? "sub.webCheckFailed" : "sub.webPending")}
        </p>
        {pending && href ? <a className="btn btn-primary" href={href}>{t("sub.shopGo")}</a> : null}
        {finished && sub ? <a className="btn btn-primary" href={sub}>{t("sub.webOpenLink")}</a> : null}
        {finished && !sub ? <p className="text-sm">{t("sub.webLinkUnavailable")}</p> : null}
        {canRetry ? <Button disabled={retrying} onClick={() => void retry()}>{t("sub.webRetryInvoice")}</Button> : null}
        {canRetry && retryFailed ? <p role="alert" className="text-sm">{t("sub.shopFail")}</p> : null}
        {pending && !canRetry ? <Button variant="ghost" onClick={() => setAttempt((n) => n + 1)}>{t("sub.shopRefresh")}</Button> : null}
        {!pending || canRetry ? <Button variant="ghost" disabled={retrying} onClick={clear}>{t(finished || canRetry ? "sub.webBack" : "common.retry")}</Button> : null}
      </section>
    );
  }

  if (!data?.offers.length) {
    if (subToken && data !== null) return null;
    return (
      <section className="glass sub-card p-4" aria-label={title}>
        <h1 className="mb-2 text-[15px] font-semibold">{title}</h1>
        {data === undefined ? <Skeleton className="h-8" /> : <p role="status">{t(data ? "sub.webOff" : "sub.shopFail")}</p>}
        {data === null ? <Button variant="ghost" onClick={() => setAttempt((n) => n + 1)}>{t("common.retry")}</Button> : null}
      </section>
    );
  }

  return (
    <Shop data={data} offers={data.offers} subRoot="" initData="" token={subToken} title={title}
      openInvoice={() => undefined} openLink={() => undefined} onRefresh={() => undefined}
      browserPay={async ({ email, ...order }) => {
        const current = { key: newKey(), subToken, order };
        // Save before opening the invoice so a reload or a provider redirect can resume.
        remember(current);
        try {
          const r = await openInvoice(current, email);
          if (!r.ok) {
            if (r.status >= 500 || r.status === 429 || r.data.code === "invoice_in_progress") setCheckout(current);
            else remember(null);
            return r.data.code ?? "invoice_failed";
          }
          setCheckout(current);
          setResult(r.data);
        } catch {
          setCheckout(current);
          return "invoice_failed";
        }
        return undefined;
      }}
    />
  );
}
