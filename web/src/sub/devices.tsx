import { Hourglass, Laptop, Layers, Pencil, Smartphone } from "lucide-react";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { Button, Pill } from "../components/ui";
import { t, tMaybe } from "../i18n";
import { DEVICE_NAME_MAX, desktopOS, deviceDetails, deviceLabel, reportedName } from "../lib/devices";
import { ago, bytes, dateShort, time } from "../lib/format";
import { request } from "./net";
import type { Device, Info } from "./types";

function deviceName(d: Device): string {
  return d.shared && !d.name ? t("sub.sharedPlace") : deviceLabel(d, t("sub.device"));
}

/**
 * The device's own name, as the subscriber writes it: up to 40 letters, empty for the
 * name the app reported. The panel checks it again; its refusal shows under the field.
 */
function RenameForm({ d, subURL, onDone }: { d: Device; subURL: string; onDone: (saved: boolean) => void }) {
  const id = useId();
  const [name, setName] = useState(d.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const clean = name.trim();
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (busy || clean === d.name) return;
    setBusy(true);
    setError("");
    try {
      const r = await request(`${subURL}/devices/${d.id}/name`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: clean }) });
      if (r.ok) {
        onDone(true);
        return;
      }
      const b = (await r.json().catch(() => ({}))) as { code?: string };
      setError((b.code && tMaybe(`sub.nameErrors.${b.code}`)) || t("sub.renameFailed"));
    } catch {
      setError(t("sub.renameFailed"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="mt-2 rounded-2xl bg-[var(--hover)] p-3" onSubmit={(e) => void save(e)} noValidate>
      <label htmlFor={id} className="text-[13px] font-medium text-[var(--ink-700)]">
        {t("sub.renameField")}
      </label>
      <input
        id={id}
        className="input mt-1"
        value={name}
        maxLength={DEVICE_NAME_MAX}
        placeholder={(d.shared ? t("sub.sharedPlace") : reportedName(d)) || t("sub.device")}
        autoComplete="off"
        autoFocus
        aria-invalid={!!error}
        aria-describedby={`${id}-note`}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && onDone(false)}
      />
      {error ? (
        <p id={`${id}-note`} className="mt-1 text-xs text-[var(--berry-600)]" role="alert">
          {error}
        </p>
      ) : (
        <p id={`${id}-note`} className="mt-1 text-xs text-[var(--ink-500)]">
          {t("sub.renameHint")}
        </p>
      )}
      <div className="mt-2 flex justify-end gap-2">
        <Button variant="ghost" size="sm" disabled={busy} onClick={() => onDone(false)}>
          {t("common.cancel")}
        </Button>
        <Button variant="primary" size="sm" type="submit" loading={busy} disabled={clean === d.name}>
          {t("common.save")}
        </Button>
      </div>
    </form>
  );
}

/** The unbind window in words: "сутки", "неделю", "10 дней". */
function period(days: number): string {
  if (days === 1) return t("sub.periodDay");
  if (days === 7) return t("sub.periodWeek");
  if (days === 30) return t("sub.periodMonth");
  return t("sub.periodDays", { n: days });
}

/** A pause in hours, whole days as days. */
function pause(hours: number): string {
  return hours % 24 === 0 ? t("sub.periodDays", { n: hours / 24 }) : t("sub.pauseHours", { n: hours });
}

/** The subscriber's own devices: each holds a place; they unbind them within the admin's rules. */
export function Devices({ info, subURL, reload, title }: { info: Info; subURL: string; reload: () => Promise<void>; title?: string }) {
  const [confirm, setConfirm] = useState<number | null>(null);
  const [renaming, setRenaming] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // The button that opened the confirmation: focus goes back to it, not to the page's top.
  const opener = useRef<number | null>(null);
  const list = info.devices ?? [];
  const full = info.device_limit > 0 && list.length >= info.device_limit;
  const wait = info.unbind_after && new Date(info.unbind_after).getTime() > Date.now() ? info.unbind_after : "";
  const returnHours = info.return_hours ?? 0;
  const left = info.unbinds_left ?? -1;
  const note = [
    t("sub.devicesNote"),
    left >= 0 && !wait ? t("sub.unbindLeft", { left, limit: info.unbind_limit ?? 0, period: period(info.unbind_days ?? 1) }) : "",
    returnHours > 0 ? t("sub.returnNote", { pause: pause(returnHours) }) : "",
  ]
    .filter(Boolean)
    .join(" ");

  useEffect(() => {
    if (confirm !== null || opener.current === null) return;
    // Cancelled: the button is back, give it the focus again.
    document.getElementById(`unbind-${opener.current}`)?.focus();
    opener.current = null;
  }, [confirm]);

  const ask = (id: number) => {
    opener.current = id;
    setRenaming(null);
    setConfirm(id);
  };

  const doneRenaming = async (id: number, saved: boolean) => {
    if (saved) await reload();
    setRenaming(null);
    document.getElementById(`rename-${id}`)?.focus();
  };

  const unbind = async (id: number) => {
    setBusy(true);
    setError("");
    try {
      const r = await request(`${subURL}/devices/${id}/unbind`, { method: "POST" });
      if (r.status === 429) {
        const b = (await r.json().catch(() => ({}))) as { unbind_after?: string };
        setError(b.unbind_after ? t("sub.unbindAfter", { date: dateShort(b.unbind_after), time: time(b.unbind_after) }) : t("sub.unbindFailed"));
      } else if (!r.ok) {
        setError(t("sub.unbindFailed"));
      }
      // The device is gone from the list (or the answer says why not): focus the heading
      // once the list is back, a button that no longer exists cannot keep it.
      opener.current = null;
      setConfirm(null);
      await reload();
      document.getElementById("devices-title")?.focus();
    } catch {
      setError(t("sub.unbindFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="glass sub-card p-4" aria-labelledby="devices-title">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h2 id="devices-title" tabIndex={-1} className="text-[15px] font-semibold">
          {title || t("sub.devicesTitle")}
        </h2>
        {info.device_limit > 0 ? <Pill tone={full ? "warn" : "ok"}>{t("sub.devicesCount", { n: list.length, limit: info.device_limit })}</Pill> : null}
      </div>
      {full ? <p className="mb-3 rounded-2xl bg-[var(--hover)] p-3 text-[13px] text-[var(--ink-700)]">{t("sub.devicesFull")}</p> : null}
      {wait && list.length > 0 ? (
        <div className="mb-3 flex items-start gap-2 rounded-2xl bg-[var(--hover)] p-3" role="status">
          <Hourglass size={18} className="mt-0.5 shrink-0 text-[var(--mikan-600)]" aria-hidden />
          <div className="text-[13px]">
            <p className="font-medium text-[var(--ink-800)]">{t("sub.unbindLimitTitle")}</p>
            <p className="text-[var(--ink-600)]">{t("sub.unbindAfter", { date: dateShort(wait), time: time(wait) })}</p>
          </div>
        </div>
      ) : null}
      {list.length === 0 ? (
        <p className="text-[13px] text-[var(--ink-500)]">{t("sub.devicesEmpty")}</p>
      ) : (
        <ul className="row-list">
          {list.map((d) => {
            const name = deviceName(d);
            const meta = deviceDetails(d, !d.shared);
            return (
              <li key={d.id} className="py-2">
                <div className="grid grid-cols-[40px_minmax(0,1fr)_auto] items-center gap-3">
                  <span className="grid h-10 w-10 place-items-center rounded-xl border border-[var(--hairline)] bg-[var(--surface-solid)] text-[var(--ink-600)]" aria-hidden>
                    {d.shared ? <Layers size={18} /> : desktopOS.test(d.os) ? <Laptop size={18} /> : <Smartphone size={18} />}
                  </span>
                  <div className="min-w-0">
                    <div className="truncate text-sm font-semibold">{name}</div>
                    <div className="text-xs break-words text-[var(--ink-500)]">
                      {meta ? `${meta} · ` : ""}
                      {d.used ? `${bytes(d.used)} · ` : ""}
                      {ago(d.last_seen)}
                    </div>
                  </div>
                  {confirm === d.id ? null : (
                    <div className="flex items-center gap-1">
                      <button
                        id={`rename-${d.id}`}
                        type="button"
                        className="icon-btn"
                        aria-label={t("sub.renameLabel", { name })}
                        aria-expanded={renaming === d.id}
                        title={t("sub.rename")}
                        onClick={() => {
                          setConfirm(null);
                          setRenaming(renaming === d.id ? null : d.id);
                        }}
                      >
                        <Pencil size={16} aria-hidden />
                      </button>
                      {wait ? null : (
                        <Button id={`unbind-${d.id}`} size="sm" disabled={busy} onClick={() => ask(d.id)} aria-label={t("sub.unbindLabel", { name })}>
                          {t("sub.unbind")}
                        </Button>
                      )}
                    </div>
                  )}
                </div>
                {renaming === d.id ? <RenameForm d={d} subURL={subURL} onDone={(saved) => void doneRenaming(d.id, saved)} /> : null}
                {confirm === d.id ? (
                  <div className="mt-2 rounded-2xl bg-[var(--hover)] p-3" role="group" aria-label={t("sub.unbindLabel", { name })}>
                    <p className="text-[13px] text-[var(--ink-700)]">{t("sub.unbindWarn")}</p>
                    {returnHours > 0 && !d.shared ? <p className="mt-1 text-[13px] font-medium text-[var(--ink-800)]">{t("sub.unbindWarnReturn", { pause: pause(returnHours) })}</p> : null}
                    <div className="mt-2 flex justify-end gap-2">
                      <Button variant="ghost" size="sm" disabled={busy} autoFocus onClick={() => setConfirm(null)}>
                        {t("common.cancel")}
                      </Button>
                      <Button variant="danger-solid" size="sm" loading={busy} onClick={() => void unbind(d.id)}>
                        {t("sub.unbindYes")}
                      </Button>
                    </div>
                  </div>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      {error ? (
        <p className="mt-2 text-[13px] text-[var(--berry-600)]" role="alert">
          {error}
        </p>
      ) : null}
      <p className="mt-2 text-xs text-[var(--ink-500)]">{note}</p>
    </section>
  );
}
