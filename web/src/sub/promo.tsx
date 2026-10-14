import { useEffect, useState } from "react";
import { Check, Gift, RefreshCw } from "lucide-react";
import { Button } from "../components/ui";
import { GiB, money } from "../lib/format";
import { t } from "../i18n";
import { hashAnchor, initData, okData, postJSON, subRoot, tokenOf } from "./net";

// The bot's "Promo codes" opens the page at #promocodes: the section is scrolled to once.
let scrolled = false;

type Item = { code: string; days: number; bytes: number; discount: number; currency: string; at: number };

export function PromoSection({ token, activeCode, onApplied, onBonusApplied, title }: { token: string; activeCode: string; onApplied: (code: string) => void; onBonusApplied: () => void; title?: string }) {
  const [code, setCode] = useState("");
  const [items, setItems] = useState<Item[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [err, setErr] = useState("");

  const load = () => postJSON<{ items: Item[] }>(`${subRoot}/tg/promo-history`, { init_data: initData })
    .then((p) => setItems(okData(p).items ?? []))
    .catch(() => undefined);

  // The history is the account's, not the subscription's: the token does not change it.
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (scrolled || hashAnchor !== "promocodes") return;
    scrolled = true;
    document.getElementById("promocodes")?.scrollIntoView({ block: "start" });
  }, []);

  const errorMessage = (code: string) => ({
    promo_try_later: t("sub.promoTryLater"),
  } as Record<string, string>)[code] ?? t("sub.promoUnavailable");

  const apply = async () => {
    if (!code.trim()) return;
    setBusy(true); setMsg(""); setErr("");
    try {
      const r = await postJSON<{ type: string; code: string; days: number; bytes: number }>(`${subRoot}/tg/promo`, { init_data: initData, token: tokenOf(token), code: code.trim() });
      const body = r.data;
      if (!r.ok) { setErr(errorMessage(body.code || "")); return; }
      if (body.type === "days") { setMsg(t("sub.promoDaysApplied", { n: body.days ?? 0 })); onApplied(""); onBonusApplied(); }
      else if (body.type === "traffic") { setMsg(t("sub.promoTrafficApplied", { n: ((body.bytes ?? 0) / GiB).toFixed(0) })); onApplied(""); onBonusApplied(); }
      // A discount is checked against the price at the checkout: the page keeps the code.
      else if (body.type === "percent" || body.type === "fixed") { onApplied(body.code || code.trim().toUpperCase()); setMsg(t("sub.promoDiscountApplied")); }
      else { setErr(t("sub.promoUnavailable")); return; }
      setCode(""); await load();
    } catch {
      setErr(t("sub.promoUnavailable"));
    } finally { setBusy(false); }
  };


  return <section id="promocodes" className="glass sub-card p-4"><div className="flex items-center gap-2"><Gift size={18} /><h2 className="text-[15px] font-semibold">{title || t("sub.promoTitle")}</h2></div><p className="mt-1 text-xs text-[var(--ink-500)]">{t("sub.promoHint")}</p>{activeCode && <div className="mt-3 flex items-center justify-between rounded-xl bg-[var(--leaf-50)] px-3 py-2 text-xs text-[var(--leaf-700)]"><span>{t("sub.promoSelected", { code: activeCode })}</span><button className="link-btn" onClick={() => { onApplied(""); setMsg(""); }} type="button">{t("sub.promoClear")}</button></div>}<div className="mt-3 flex flex-wrap gap-2"><input className="input min-w-0 flex-1" value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} placeholder="WELCOME30" onKeyDown={(e) => { if (e.key === "Enter") void apply(); }} /><Button variant="primary" loading={busy} onClick={() => void apply()}>{t("sub.promoApply")}</Button></div>{msg && <p className="mt-3 flex items-center gap-2 text-xs text-[var(--leaf-600)]"><Check size={14} />{msg}</p>}{err && <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">{err}</p>}<div className="mt-4 border-t border-[var(--hairline)] pt-3"><div className="mb-2 flex items-center justify-between text-xs font-semibold"><span>{t("sub.promoHistory")}</span><button className="link-btn" onClick={() => void load()} aria-label={t("common.retry")}><RefreshCw size={13} /></button></div>{items.length === 0 ? <div className="text-xs text-[var(--ink-500)]">{t("sub.promoNoHistory")}</div> : <div className="flex flex-col gap-2">{items.map((item, i) => <div key={`${item.code}-${item.at}-${i}`} className="text-xs"><b>{item.code}</b> · {new Date(item.at * 1000).toLocaleString()}{item.days ? ` · +${item.days} ${t("promocodes.daysUnit")}` : ""}{item.bytes ? ` · +${(item.bytes / GiB).toFixed(0)} ${t("promocodes.trafficUnit")}` : ""}{item.discount ? ` · −${money(item.discount, item.currency)}` : ""}</div>)}</div>}</div></section>;
}
