import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database } from "lucide-react";
import { useEffect, useId, useState, type FormEvent } from "react";
import { api, errorText, unwrap } from "../../api/client";
import { qk } from "../../api/hooks";
import { Drawer, DrawerFooter } from "../../components/overlay";
import { SwitchRow } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, Field } from "../../components/ui";
import { t } from "../../i18n";
import { Row } from "./addon-row";

function useAddonStorage() {
  return useQuery({ queryKey: qk.addonStorage, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/addon-storage", { signal })) });
}

/**
 * How much each addon may keep in its own store: 256 MiB unless the admin sets another bound,
 * or none at all past a warning about the disk and the backups.
 */
export function StorageRow() {
  const q = useAddonStorage();
  const [open, setOpen] = useState(false);
  const v = q.data;
  const state = !v
    ? q.isError
      ? { tone: "bad" as const, text: t("common.loadFailed") }
      : undefined
    : v.unlimited
      ? { tone: "warn" as const, text: t("addons.storage.unlimitedShort") }
      : { tone: "ok" as const, text: t("addons.storage.limitShort", { mib: v.limit_mib }) };
  return (
    <>
      <Row
        icon={Database}
        name={t("addons.storage.title")}
        sub={t("addons.storage.sub")}
        state={state}
        actions={
          <Button size="sm" variant="glass" onClick={() => setOpen(true)} disabled={!v}>
            {t("addons.configure")}
          </Button>
        }
      />
      {v ? <StorageDrawer open={open} onOpenChange={setOpen} unlimited={v.unlimited} limit={v.limit_mib} fallback={v.default_mib} /> : null}
    </>
  );
}

function StorageDrawer({ open, onOpenChange, unlimited, limit, fallback }: { open: boolean; onOpenChange: (v: boolean) => void; unlimited: boolean; limit: number; fallback: number }) {
  const qc = useQueryClient();
  const toast = useToast();
  const form = useId();
  const [free, setFree] = useState(unlimited);
  const [mib, setMib] = useState(String(limit));
  const [error, setError] = useState("");
  useEffect(() => {
    if (open) {
      setFree(unlimited);
      setMib(String(limit));
      setError("");
    }
  }, [open, unlimited, limit]);
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/api/v1/addon-storage", { body: free ? { unlimited: true } : { limit_mib: Number(mib) } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.addonStorage, v);
      toast.ok(t("addons.storage.saved"));
      onOpenChange(false);
    },
    onError: (e) => setError(errorText(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const n = Number(mib);
    if (!free && (!Number.isInteger(n) || n < 1 || n > 1048576)) {
      setError(t("addons.storage.badLimit"));
      return;
    }
    setError("");
    save.mutate();
  };
  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      title={t("addons.storage.title")}
      meta={t("addons.storage.lead", { mib: fallback })}
      footer={
        <DrawerFooter form={form} onCancel={() => onOpenChange(false)} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id={form} onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <Field label={t("addons.storage.limit")} htmlFor={`${form}-mib`} hint={t("addons.storage.limitHint", { mib: fallback })} error={free ? undefined : error || undefined}>
          <input
            id={`${form}-mib`}
            className="input"
            type="number"
            inputMode="numeric"
            min={1}
            max={1048576}
            step={1}
            value={mib}
            disabled={free}
            onChange={(e) => {
              setMib(e.target.value);
              setError("");
            }}
          />
        </Field>
        <SwitchRow label={t("addons.storage.unlimited")} sub={t("addons.storage.unlimitedSub")} warn checked={free} onChange={setFree} />
        {free ? (
          <div className="banner warn" role="alert">
            {t("addons.storage.unlimitedWarning")}
          </div>
        ) : null}
        {free && error ? (
          <div className="banner bad" role="alert">
            {error}
          </div>
        ) : null}
      </form>
    </Drawer>
  );
}
