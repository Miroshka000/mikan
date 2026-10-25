// The rules of the subscriptions' lifecycle: what a period leaves of its traffic, and when the
// panel removes a subscription nobody uses. The removal shows whom it reaches before it is on.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useInactive, useLifecycle } from "../../api/hooks";
import { Confirm, Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { SwitchRow } from "../../components/switch";
import { useToast } from "../../components/toast";
import { ErrorState, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { ago, dateShort, days, num } from "../../lib/format";
import { LimitPicker } from "./user/limit-picker";
import { Section } from "./user/section";

type Lifecycle = Schemas["Lifecycle"];
type Removal = Schemas["RemovalRules"];

/** How many of the subscriptions the removal reaches a confirmation names. */
const NAMES_SHOWN = 5;

export function LifecycleDrawer({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const rules = useLifecycle();
  return (
    <Drawer open={open} onOpenChange={onOpenChange} title={t("lifecycle.title")} meta={t("lifecycle.meta")}>
      <QueryBoundary
        query={rules}
        pending={
          <div className="space-y-4 pt-5">
            <Skeleton style={{ height: 120, borderRadius: 16 }} />
            <Skeleton style={{ height: 200, borderRadius: 16 }} />
          </div>
        }
      >
        {(l) => <LifecycleBody l={l} />}
      </QueryBoundary>
    </Drawer>
  );
}

function useSaveLifecycle() {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (body: Lifecycle) => unwrap(api.PUT("/api/v1/lifecycle", { body })),
    onSuccess: (l) => {
      qc.setQueryData(qk.lifecycle, l);
      toast.ok(t("lifecycle.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
}

function LifecycleBody({ l }: { l: Lifecycle }) {
  const save = useSaveLifecycle();
  // A removal turned on, or made to reach further, waits for the admin to see whom it takes.
  const [asked, setAsked] = useState<Removal | null>(null);
  const setRemoval = (r: Removal, done?: () => void) => {
    if (r.on && (!l.removal.on || r.days < l.removal.days)) {
      setAsked(r);
      done?.();
      return;
    }
    save.mutate({ ...l, removal: r }, { onSuccess: done });
  };
  return (
    <div className="pt-5">
      <Section title={t("lifecycle.carryTitle")}>
        <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("lifecycle.carryText")}</p>
        <SwitchRow
          className="py-0"
          label={t("lifecycle.carryOn")}
          sub={t("lifecycle.carryOnSub")}
          checked={l.carry.on}
          disabled={save.isPending}
          onChange={(on) => save.mutate({ ...l, carry: { ...l.carry, on } })}
        />
        {l.carry.on ? (
          <div className="mt-3">
            <LimitPicker
              label={t("lifecycle.carryCap")}
              value={l.carry.cap_percent}
              presets={[25, 50, 100]}
              min={1}
              max={100}
              unlimited={false}
              format={(n) => `${n}%`}
              fieldUnit="%"
              busy={save.isPending}
              onChange={(n, done) => save.mutate({ ...l, carry: { ...l.carry, cap_percent: n ?? 100 } }, { onSuccess: done })}
            />
            <p className="mt-2 text-xs text-[var(--ink-500)]">{t("lifecycle.carryCapHint", { n: l.carry.cap_percent })}</p>
          </div>
        ) : null}
        <p className="mt-3 text-xs text-[var(--ink-500)]">{t("lifecycle.carryTariffs")}</p>
      </Section>

      <Section title={t("lifecycle.removalTitle")}>
        <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("lifecycle.removalText")}</p>
        <SwitchRow
          className="py-0"
          label={t("lifecycle.removalOn")}
          sub={t("lifecycle.removalOnSub")}
          warn={l.removal.on}
          checked={l.removal.on}
          disabled={save.isPending}
          onChange={(on) => setRemoval({ ...l.removal, on })}
        />
        <div className="mt-3">
          <LimitPicker
            label={t("lifecycle.removalDays")}
            value={l.removal.days}
            presets={[14, 30, 90, 180]}
            min={7}
            max={3650}
            unlimited={false}
            format={days}
            fieldUnit={t("lifecycle.daysUnit")}
            busy={save.isPending}
            onChange={(n, done) => setRemoval({ ...l.removal, days: n ?? l.removal.days }, done)}
          />
        </div>
        <InactiveList days={l.removal.days} on={l.removal.on} />
      </Section>
      {asked ? <ConfirmRemoval l={l} removal={asked} saving={save.isPending} onClose={() => setAsked(null)} onConfirm={() => save.mutate({ ...l, removal: asked }, { onSuccess: () => setAsked(null) })} /> : null}
    </div>
  );
}

/** Whom the removal reaches with days: the ones it removes and the ones it keeps, and why. */
function InactiveList({ days: n, on }: { days: number; on: boolean }) {
  const list = useInactive(n);
  return (
    <div className="mt-4" aria-busy={list.isFetching}>
      <div className="mb-2 text-[13px] font-semibold">{t("lifecycle.reachTitle")}</div>
      {list.isPending ? (
        <div className="flex flex-col gap-2" role="status" aria-label={t("lifecycle.reachLoading")}>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} style={{ height: 40, borderRadius: 12 }} />
          ))}
        </div>
      ) : list.isError ? (
        <ErrorState text={errorText(list.error)} onRetry={() => void list.refetch()} />
      ) : list.data.due + list.data.kept === 0 ? (
        <p className="panel-soft p-3 text-[13px] text-[var(--ink-600)]">{t("lifecycle.reachNone", { days: days(n) })}</p>
      ) : (
        <>
          <p className="mb-2 text-xs text-[var(--ink-600)]" role="status">
            {on ? t("lifecycle.reachOn", { due: num(list.data.due), kept: num(list.data.kept) }) : t("lifecycle.reachOff", { due: num(list.data.due), kept: num(list.data.kept) })}
          </p>
          <ul className="row-list rounded-2xl border border-[var(--hairline)] px-3">
            {list.data.items.map((u) => (
              <li key={u.user_id} className="flex items-center justify-between gap-3 py-2">
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-medium">{u.user_name}</div>
                  <div className="text-xs text-[var(--ink-500)]">
                    {t("lifecycle.endedOn", { date: dateShort(u.expires_at) })} · {u.online_at ? t("lifecycle.seen", { ago: ago(u.online_at) }) : t("lifecycle.neverSeen")}
                  </div>
                </div>
                {u.keep ? <Pill tone="ok">{t(`lifecycle.keep.${u.keep}`)}</Pill> : <Pill tone="bad">{t("lifecycle.goes")}</Pill>}
              </li>
            ))}
          </ul>
          {list.data.due + list.data.kept > list.data.items.length ? <p className="mt-2 text-xs text-[var(--ink-500)]">{t("lifecycle.reachMore", { n: num(list.data.due + list.data.kept - list.data.items.length) })}</p> : null}
        </>
      )}
    </div>
  );
}

/** The removal turned on (or made to reach further) only once the admin saw whom it takes. */
function ConfirmRemoval({ l, removal, saving, onClose, onConfirm }: { l: Lifecycle; removal: Removal; saving: boolean; onClose: () => void; onConfirm: () => void }) {
  const list = useInactive(removal.days);
  const due = list.data?.items.filter((u) => !u.keep) ?? [];
  const names = due
    .slice(0, NAMES_SHOWN)
    .map((u) => u.user_name)
    .join(", ");
  return (
    <Confirm
      open
      onOpenChange={(v) => !v && onClose()}
      title={l.removal.on ? t("lifecycle.confirmFurtherTitle", { days: days(removal.days) }) : t("lifecycle.confirmTitle", { days: days(removal.days) })}
      text={
        list.data
          ? list.data.due === 0
            ? t("lifecycle.confirmNobody")
            : `${t("lifecycle.confirmText", { n: list.data.due })} ${t("lifecycle.confirmWho", { names: list.data.due > NAMES_SHOWN ? `${names}…` : names })}`
          : t("lifecycle.reachLoading")
      }
      confirm={t("lifecycle.confirm")}
      danger
      loading={saving}
      confirmDisabled={!list.data}
      onConfirm={onConfirm}
    >
      {list.isError ? (
        <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
          {errorText(list.error)}
        </p>
      ) : null}
      {list.data && list.data.kept > 0 ? <p className="mt-3 text-xs text-[var(--ink-600)]">{t("lifecycle.confirmKept", { n: list.data.kept })}</p> : null}
    </Confirm>
  );
}
