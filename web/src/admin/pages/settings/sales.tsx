import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { errorText } from "../../../api/client";
import { usePaymentSettings } from "../../../api/hooks";
import { useSellingSwitch } from "../../../api/selling";
import { CardHead } from "../../../components/layout";
import { SwitchRow } from "../../../components/switch";
import { ErrorState, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";

/**
 * Whether the panel sells subscriptions at all. Off, the Payments section leaves the menu:
 * this card is then the way back to it.
 */
export function SalesCard() {
  const settings = usePaymentSettings();
  const save = useSellingSwitch();
  const on = settings.data?.enabled === true;
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <CardHead
        title={t("settings.sales")}
        sub={t("settings.salesSub")}
        aside={
          on ? (
            <Link to="/payments" className="btn btn-glass btn-sm">
              {t("settings.salesOpen")} <ChevronRight size={16} aria-hidden />
            </Link>
          ) : null
        }
      />
      {settings.isPending ? (
        <Skeleton style={{ height: 56 }} />
      ) : settings.isError ? (
        <ErrorState text={errorText(settings.error)} onRetry={() => void settings.refetch()} />
      ) : (
        <SwitchRow label={t("settings.salesSwitch")} sub={on ? t("settings.salesOnSub") : t("settings.salesOffSub")} checked={on} disabled={save.isPending} onChange={(v) => save.mutate(v)} />
      )}
    </section>
  );
}
