import { Check } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { ApiError, errorText } from "../../../api/client";
import { userActions, useTariffs, useUserMutation } from "../../../api/hooks";
import { Drawer, DrawerFooter } from "../../../components/overlay";
import { MAX_TAGS, parseTags } from "../../../components/tags";
import { useToast } from "../../../components/toast";
import { Button, Field, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { tariffSummary } from "../tariffs";
import { BatchNameFields, MAX_BATCH, MAX_START } from "./batch-names";
import { BatchResult } from "./batch-result";
import { defaultTariffId, draftOf, emptyDraft, hasNumber, specOf, type BatchUser, type Draft, type Template } from "./model";
import { OverTariff, Placement } from "./spec-fields";
import { TemplateBar } from "./template-bar";

type Mode = "one" | "many";
type Errors = Partial<Record<string, string>>;

/**
 * A new client, or a batch of them under one name pattern: a template of clients fills the
 * form, and what the form holds is saved as a template. One client is opened as soon as it is
 * made, its link copied; a batch shows every client it made with the links to hand out.
 */
export function CreateUserDrawer({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (v: boolean) => void; onCreated: (id: number) => void }) {
  const tariffs = useTariffs();
  const toast = useToast();
  const create = useUserMutation(userActions.create);
  const batch = useUserMutation(userActions.batch);
  const { reset: resetCreate } = create;
  const { reset: resetBatch } = batch;
  const [mode, setMode] = useState<Mode>("one");
  const [picked, setPicked] = useState<Template | null>(null);
  const [draft, setDraft] = useState<Draft>(() => emptyDraft());
  const [name, setName] = useState("");
  const [contact, setContact] = useState("");
  const [count, setCount] = useState("10");
  const [start, setStart] = useState("1");
  const [errors, setErrors] = useState<Errors>({});
  const [made, setMade] = useState<BatchUser[] | null>(null);
  // Until a tariff is picked the form holds the usual one (defaultTariffId).
  const form = draft.tariffId !== undefined ? draft : { ...draft, tariffId: defaultTariffId(tariffs.data) };
  const tariff = tariffs.data?.find((tr) => tr.id === form.tariffId);

  useEffect(() => {
    if (!open) return;
    setMode("one");
    setPicked(null);
    setDraft(emptyDraft());
    setName("");
    setContact("");
    setCount("10");
    setStart("1");
    setErrors({});
    setMade(null);
    resetCreate();
    resetBatch();
  }, [open, resetCreate, resetBatch]);

  const pick = (tpl: Template | null) => {
    setPicked(tpl);
    setDraft(tpl ? draftOf(tpl) : emptyDraft(form.tariffId));
    setErrors({});
  };

  // A refusal of the names (taken, no number) says nothing any more once they change.
  const namesChanged = () => setErrors((e) => ({ ...e, name_pattern: undefined, count: undefined, start: undefined }));

  const fail = (err: unknown) => {
    if (err instanceof ApiError && Object.keys(err.fields).length) setErrors(err.fields);
    else toast.error(errorText(err));
  };

  const n = Number(count);
  const from = Number(start);
  const countOk = n >= 1 && n <= MAX_BATCH;
  const startOk = start !== "" && from <= MAX_START;
  const check = (): Errors => {
    const errs: Errors = {};
    // A template's tariff archived since is no longer offered: the admin picks another.
    if (!tariff) errs.tariff_id = t(form.tariffId !== undefined && tariffs.data ? "userCreate.tariffGone" : "userCreate.tariffRequired");
    if (mode === "one" && !name.trim()) errs.name = t("userCreate.nameRequired");
    if (mode === "many" && !countOk) errs.count = t("errors.api.batch_count");
    if (mode === "many" && !startOk) errs.start = t("userCreate.startBad");
    if (mode === "many" && !hasNumber(form.pattern)) errs.name_pattern = t("errors.api.name_pattern_number");
    if (parseTags(form.tags).length > MAX_TAGS) errs.tags = t("tags.many", { n: MAX_TAGS });
    return errs;
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errs = check();
    setErrors(errs);
    if (Object.keys(errs).length) return;
    const spec = { ...specOf(form), template_id: picked?.id };
    if (mode === "many") {
      batch.mutate({ ...spec, count: n, start: from, name_pattern: form.pattern.trim() }, { onSuccess: (r) => setMade(r.users), onError: fail });
      return;
    }
    create.mutate(
      { ...spec, name: name.trim(), contact: contact.trim() || undefined },
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
        onError: fail,
      },
    );
  };

  const footer = made ? (
    <>
      <Button variant="ghost" onClick={() => setMade(null)}>
        {t("userCreate.batchAgain")}
      </Button>
      <Button variant="primary" onClick={() => onOpenChange(false)}>
        {t("userCreate.batchClose")}
      </Button>
    </>
  ) : (
    <DrawerFooter form="create-user" onCancel={() => onOpenChange(false)} loading={create.isPending || batch.isPending}>
      <Check size={18} aria-hidden /> {mode === "one" ? t("userCreate.submit") : t("userCreate.submitMany", { n: countOk ? n : 0 })}
    </DrawerFooter>
  );
  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      title={made ? t("userCreate.batchTitle") : t("userCreate.title")}
      meta={made ? undefined : mode === "one" ? t("userCreate.meta") : t("userCreate.metaMany")}
      footer={footer}
    >
      {made ? (
        <BatchResult users={made} />
      ) : (
        <div className="flex flex-col gap-4 pt-5">
          <Segmented
            label={t("userCreate.mode")}
            value={mode}
            onChange={(m) => {
              setMode(m);
              setErrors({});
            }}
            block
            options={[
              { value: "one", label: t("userCreate.modeOne") },
              { value: "many", label: t("userCreate.modeMany") },
            ]}
          />
          {/* Out of the form: the template's own dialog is a form of its own. */}
          <TemplateBar picked={picked} onPick={pick} draft={form} />
          <form id="create-user" onSubmit={submit} className="flex flex-col gap-4" noValidate>
            <div>
              {mode === "one" ? (
                <>
                  <Field label={t("userCreate.name")} htmlFor="nu-name" hint={t("userCreate.nameHint")} error={errors.name}>
                    <input id="nu-name" className="input" placeholder={t("userCreate.namePlaceholder")} value={name} onChange={(e) => setName(e.target.value)} aria-invalid={!!errors.name} maxLength={100} autoFocus autoComplete="off" />
                  </Field>
                  <Field label={t("userCreate.contact")} htmlFor="nu-contact" hint={t("userCreate.contactHint")}>
                    <input id="nu-contact" className="input" placeholder="@telegram" value={contact} onChange={(e) => setContact(e.target.value)} maxLength={100} autoComplete="off" />
                  </Field>
                </>
              ) : (
                <BatchNameFields
                  count={count}
                  start={start}
                  pattern={form.pattern}
                  errors={errors}
                  onCount={(v) => {
                    setCount(v);
                    namesChanged();
                  }}
                  onStart={(v) => {
                    setStart(v);
                    namesChanged();
                  }}
                  onPattern={(pattern) => {
                    setDraft((d) => ({ ...d, pattern }));
                    namesChanged();
                  }}
                />
              )}
              <Field label={t("userCreate.tariff")} error={errors.tariff_id} hint={t("userCreate.tariffHint")}>
                {tariffs.isPending ? (
                  <div className="grid grid-cols-2 gap-2">
                    <Skeleton style={{ height: 76, borderRadius: 16 }} />
                    <Skeleton style={{ height: 76, borderRadius: 16 }} />
                  </div>
                ) : (
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2" role="radiogroup" aria-label={t("userCreate.tariff")}>
                    {(tariffs.data ?? []).map((tr) => (
                      <button
                        key={tr.id}
                        type="button"
                        role="radio"
                        aria-checked={form.tariffId === tr.id}
                        className="opt"
                        // Another tariff has a word of its own on the wait.
                        onClick={() => setDraft((d) => ({ ...d, tariffId: tr.id, wait: undefined }))}
                      >
                        <span className="font-semibold">{tr.name}</span>
                        <span className="text-xs text-[var(--ink-500)]">{tariffSummary(tr)}</span>
                        {tr.price_label ? <span className="mt-1 text-[13px] font-medium text-[var(--mikan-700)]">{tr.price_label}</span> : null}
                      </button>
                    ))}
                  </div>
                )}
              </Field>
            </div>
            <div>
              <OverTariff draft={form} setDraft={setDraft} tariff={tariff} />
              <Placement draft={form} setDraft={setDraft} errors={errors} />
            </div>
          </form>
        </div>
      )}
    </Drawer>
  );
}
