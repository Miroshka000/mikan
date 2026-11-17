// When traffic is reset, as the tariff's form and the client's card ask it: every 30 days,
// monthly on the billing day (the 1st without one), or never.
import { Segmented } from "../../components/ui";
import { t } from "../../i18n";
import type { Reset } from "./reset-rules";

export function ResetPicker({ value, onChange, billingDay, disabled }: { value: Reset; onChange: (v: Reset) => void; billingDay: number | null; disabled?: boolean }) {
  return (
    <Segmented
      label={t("tariffs.reset")}
      value={value}
      onChange={onChange}
      disabled={disabled}
      options={[
        { value: "period", label: t("tariffs.resetEvery30") },
        { value: "month_start", label: billingDay != null ? t("tariffs.resetOnDayShort", { d: billingDay }) : t("tariffs.resetMonthStart") },
        { value: "none", label: t("tariffs.resetNever") },
      ]}
    />
  );
}
