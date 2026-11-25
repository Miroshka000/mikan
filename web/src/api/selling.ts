import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useToast } from "../components/toast";
import { t } from "../i18n";
import { api, errorText, unwrap } from "./client";
import { qk } from "./hooks";

/**
 * Turns selling on or off for the whole panel: the bot and the Mini App sell only while it is
 * on, and the menu lists Payments only then. One switch, wherever it is drawn.
 */
export function useSellingSwitch() {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (enabled: boolean) => unwrap(api.PATCH("/api/v1/payments/settings", { body: { enabled } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.paymentSettings, v);
      toast.ok(v.enabled ? t("settings.salesOnToast") : t("settings.salesOffToast"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
}
