// The apps the page offers on each platform, and the QR code.
import { ArrowDown, ArrowUp } from "lucide-react";
import { Switch, SwitchRow } from "../../../../components/switch";
import { Field, Segmented } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { APPS, type Platform } from "../../../../sub/apps";
import { orderApps, PLATFORMS } from "../../../../sub/page";
import { PLATFORM_NAMES } from "../sub-docs";
import { Card, type Config, type Set } from "./parts";
import { Select } from "../../../../components/select";

export function AppsSection({ config, set, announce, platform, setPlatform }: { config: Config; set: Set; announce: (text: string) => void; platform: Platform; setPlatform: (p: Platform) => void }) {
  const apps = config.apps;
  const list = apps[platform];
  // Every app of the catalogue, hidden ones too, in the order the page shows them.
  const all = orderApps(APPS[platform], { order: list.order, hidden: [] });
  const setList = (next: Partial<typeof list>) => set((c) => ({ ...c, apps: { ...c.apps, [platform]: { ...c.apps[platform], ...next } } }));
  const move = (i: number, step: -1 | 1) => {
    const names = all.map((a) => a.name);
    const [name] = names.splice(i, 1);
    names.splice(i + step, 0, name!);
    setList({ order: names });
    announce(t("settings.page.moved", { name: name!, n: i + step + 1, total: names.length }));
    window.requestAnimationFrame(() => {
      const want = document.getElementById(`app-${step < 0 ? "up" : "down"}-${name}`) as HTMLButtonElement | null;
      (want && !want.disabled ? want : document.getElementById(`app-${step < 0 ? "down" : "up"}-${name}`))?.focus();
    });
  };
  const shownCount = all.filter((a) => !list.hidden.includes(a.name)).length;
  return (
    <Card title={t("settings.page.apps")} sub={t("settings.page.appsSub")}>
      <div className="grid gap-x-3 sm:grid-cols-2">
        <Field label={t("settings.page.appsDefault")} htmlFor="sp-platform" hint={t("settings.page.appsDefaultHint")}>
          <Select id="sp-platform" className="input" value={apps.platform} onChange={(e) => set((c) => ({ ...c, apps: { ...c.apps, platform: e.target.value as Config["apps"]["platform"] } }))}>
            <option value="auto">{t("settings.page.appsAuto")}</option>
            {PLATFORMS.map((p) => (
              <option key={p} value={p}>
                {PLATFORM_NAMES[p]}
              </option>
            ))}
          </Select>
        </Field>
        <SwitchRow label={t("settings.page.qr")} sub={t("settings.page.qrSub")} checked={apps.qr} onChange={(qr) => set((c) => ({ ...c, apps: { ...c.apps, qr } }))} />
      </div>
      <div className="mb-3 flex gap-1 overflow-x-auto" role="group" aria-label={t("sub.platform")}>
        <Segmented label={t("sub.platform")} value={platform} onChange={setPlatform} options={PLATFORMS.map((p) => ({ value: p, label: PLATFORM_NAMES[p]! }))} />
      </div>
      <ul className="flex flex-col gap-2">
        {all.map((a, i) => {
          const hidden = list.hidden.includes(a.name);
          return (
            <li key={a.name} className="panel-soft flex items-center gap-3 p-3">
              <Switch
                checked={!hidden}
                label={t("settings.page.showApp", { name: a.name })}
                onChange={(on) => setList({ hidden: on ? list.hidden.filter((n) => n !== a.name) : [...list.hidden, a.name] })}
              />
              <div className={`min-w-0 flex-1 ${hidden ? "opacity-60" : ""}`}>
                <div className="text-[13px] font-semibold">{a.name}</div>
                <div className="text-xs text-[var(--ink-500)]">{t(`sub.notes.${a.note}`)}</div>
              </div>
              <button type="button" id={`app-up-${a.name}`} className="icon-btn" disabled={i === 0} onClick={() => move(i, -1)} aria-label={t("settings.page.moveUp", { name: a.name })}>
                <ArrowUp size={16} />
              </button>
              <button type="button" id={`app-down-${a.name}`} className="icon-btn" disabled={i === all.length - 1} onClick={() => move(i, 1)} aria-label={t("settings.page.moveDown", { name: a.name })}>
                <ArrowDown size={16} />
              </button>
            </li>
          );
        })}
      </ul>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{shownCount === 0 ? t("settings.page.appsNone") : t("settings.page.appsNote")}</p>
    </Card>
  );
}
