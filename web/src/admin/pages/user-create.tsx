import clsx from "clsx";
import { Check } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { ApiError, errorText } from "../../api/client";
import { userActions, useTariffs, useUserMutation } from "../../api/hooks";
import { Drawer, DrawerFooter } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { Field, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { tariffSummary } from "./tariffs";

export function CreateUserDrawer({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (v: boolean) => void; onCreated: (id: number) => void }) {
  const tariffs = useTariffs();
  const toast = useToast();
  const create = useUserMutation(userActions.create);
  const [name, setName] = useState("");
  const [contact, setContact] = useState("");
  const [tariffId, setTariffId] = useState<number>();
  const [errors, setErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    if (!open) return;
    setName("");
    setContact("");
    setErrors({});
    create.reset();
    // Reset only when the drawer opens; `create` changes identity on every render.
  }, [open]);

  // Preselects the second tariff: of the ones a new panel starts with (Trial, Standard,
  // Unlimited) the usual choice for a customer is Standard, not the 3-day trial. With one
  // tariff only, that one.
  useEffect(() => {
    if (tariffId === undefined && tariffs.data?.length) setTariffId(tariffs.data[Math.min(1, tariffs.data.length - 1)]!.id);
  }, [tariffs.data, tariffId]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setErrors({ name: t("userCreate.nameRequired") });
      return;
    }
    if (!tariffId) {
      setErrors({ tariff_id: t("userCreate.tariffRequired") });
      return;
    }
    create.mutate(
      { name: name.trim(), contact: contact.trim() || undefined, tariff_id: tariffId },
      {
        onSuccess: async (u) => {
          try {
            await navigator.clipboard.writeText(u.sub_url);
            toast.ok(t("userCreate.createdCopied", { name: u.name }));
          } catch {
            toast.ok(t("userCreate.created", { name: u.name }));
          }
          onCreated(u.id);
        },
        onError: (err) => {
          if (err instanceof ApiError && Object.keys(err.fields).length) setErrors(err.fields);
          else toast.error(errorText(err));
        },
      },
    );
  };

  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      title={t("userCreate.title")}
      meta={t("userCreate.meta")}
      footer={
        <DrawerFooter form="create-user" onCancel={() => onOpenChange(false)} loading={create.isPending}>
          <Check size={18} aria-hidden /> {t("userCreate.submit")}
        </DrawerFooter>
      }
    >
      <form id="create-user" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("userCreate.name")} htmlFor="nu-name" hint={t("userCreate.nameHint")} error={errors.name}>
          <input id="nu-name" className="input" placeholder={t("userCreate.namePlaceholder")} value={name} onChange={(e) => setName(e.target.value)} aria-invalid={!!errors.name} maxLength={100} autoFocus autoComplete="off" />
        </Field>
        <Field label={t("userCreate.contact")} htmlFor="nu-contact" hint={t("userCreate.contactHint")}>
          <input id="nu-contact" className="input" placeholder="@telegram" value={contact} onChange={(e) => setContact(e.target.value)} maxLength={100} autoComplete="off" />
        </Field>
        <Field label={t("userCreate.tariff")} error={errors.tariff_id} hint={t("userCreate.tariffHint")}>
          {tariffs.isPending ? (
            <div className="grid grid-cols-2 gap-2">
              <Skeleton style={{ height: 76, borderRadius: 16 }} />
              <Skeleton style={{ height: 76, borderRadius: 16 }} />
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2" role="radiogroup" aria-label={t("userCreate.tariff")}>
              {(tariffs.data ?? []).map((tr) => (
                <button key={tr.id} type="button" role="radio" aria-checked={tariffId === tr.id} className={clsx("opt")} onClick={() => setTariffId(tr.id)}>
                  <span className="font-semibold">{tr.name}</span>
                  <span className="text-xs text-[var(--ink-500)]">{tariffSummary(tr)}</span>
                  {tr.price_label ? <span className="mt-1 text-[13px] font-medium text-[var(--mikan-700)]">{tr.price_label}</span> : null}
                </button>
              ))}
            </div>
          )}
        </Field>
      </form>
    </Drawer>
  );
}
