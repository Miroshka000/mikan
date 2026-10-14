import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../../../api/client";
import { onFormError } from "../../../lib/fields";
import { qk } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { t } from "../../../i18n";

export function useSaveSettings() {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (body: Schemas["PatchSettingsInputBody"]) => unwrap(api.PATCH("/api/v1/settings", { body })),
    onSuccess: (data) => {
      qc.setQueryData(qk.settings, data);
      void qc.invalidateQueries({ queryKey: qk.users });
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      toast.ok(t("settings.saved"));
    },
    onError: (e) => {
      onFormError(e, toast);
    },
  });
}
