import { Drawer } from "../../../../components/overlay";
import { Pill } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { hasRoom, type Core, type View } from "./model";

/** What a new template is: a simple one (a Clash template of switches for every app) or the text of a core. */
export type NewKind = "simple" | Core;

const KINDS: { kind: NewKind; core: Core }[] = [
  { kind: "simple", core: "mihomo" },
  { kind: "mihomo", core: "mihomo" },
  { kind: "singbox", core: "singbox" },
  { kind: "xray", core: "xray" },
];

/** The choice of a new template's kind, the simple one first and recommended; a core with no room left is shown, closed. */
export function NewTemplate({ v, open, onOpenChange, onPick }: { v: View; open: boolean; onOpenChange: (o: boolean) => void; onPick: (k: NewKind) => void }) {
  useLocale();
  return (
    <Drawer open={open} onOpenChange={onOpenChange} title={t("settings.subTemplates.new")}>
      <p className="pt-4 text-sm text-[var(--ink-500)]">{t("settings.subTemplates.newKinds.lead")}</p>
      <div className="flex flex-col gap-2 pt-4">
        {KINDS.map(({ kind, core }) => {
          const room = hasRoom(v, core);
          return (
            <button key={kind} type="button" className="opt" disabled={!room} style={room ? undefined : { cursor: "not-allowed" }} onClick={() => onPick(kind)}>
              <span className="flex flex-wrap items-center gap-2 font-semibold">
                {t(`settings.subTemplates.newKinds.${kind}`)}
                {kind === "simple" ? <Pill tone="ok">{t("settings.subTemplates.newKinds.recommended")}</Pill> : null}
              </span>
              <span className="text-xs text-[var(--ink-500)]">{room ? t(`settings.subTemplates.newKinds.${kind}Sub`) : t("settings.subTemplates.newKinds.full", { max: v.max })}</span>
            </button>
          );
        })}
      </div>
    </Drawer>
  );
}
