import { ShieldAlert } from "lucide-react";
import type { ReactNode } from "react";
import { Pill } from "../../components/ui";
import { t } from "../../i18n";
import { unofficialLabel, type MarketOrigin } from "./market-common";

// The small pieces the addons tabs, their dialogs and the sources drawer all show for an
// addon that mikan has not checked: the warning with the shield and the mark of its source.

/** A warning (or an error) about trust: the shield, then the words. The parent supplies the margin. */
export function ShieldBanner({ tone = "warn", role, className, children }: { tone?: "warn" | "err"; role?: "note" | "alert" | "status"; className?: string; children: ReactNode }) {
  return (
    <div className={`banner ${tone} items-start${className ? ` ${className}` : ""}`} role={role ?? (tone === "err" ? "alert" : "note")}>
      <ShieldAlert size={16} className="mt-0.5 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1">{children}</span>
    </div>
  );
}

/** The badge of an addon from a source: whose it is, and what that does not vouch for in its hint. */
export function OriginMark({ origin }: { origin: MarketOrigin }) {
  return (
    <span title={t("market.unofficialHint", { title: origin.title })}>
      <Pill tone="warn">{unofficialLabel(origin)}</Pill>
    </span>
  );
}
