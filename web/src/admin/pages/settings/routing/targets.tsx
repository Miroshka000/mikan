import { useNodes } from "../../../../api/hooks";
import { Select } from "../../../../components/select";
import { t } from "../../../../i18n";
import { TARGETS } from "./model";

/** A place a service or a list goes: "" (as the mode sends it), vpn, direct, block or node:<id>. */
export type Target = { id: string; label: string; node?: boolean };

/** Every target in the order the panel shows them: the policies, then a server of each node. */
export function useTargets(): { targets: Target[]; loading: boolean } {
  const nodes = useNodes();
  const nodeName = (n: { id: number; name: string; local: boolean }) => t("settings.routesOnServer", { name: n.name || (n.local ? t("settings.routesThisServer") : `#${n.id}`) });
  return {
    targets: [...TARGETS.map((x) => ({ id: x.id, label: t(x.label) })), ...(nodes.data ?? []).map((n) => ({ id: `node:${n.id}`, label: nodeName(n), node: true }))],
    loading: nodes.isPending,
  };
}

/** The select of a target; follow: with "as the mode sends it" (a service has it, a list does not). */
export function TargetSelect({ id, value, onChange, label, follow = true, className }: { id: string; value: string; onChange: (v: string) => void; label?: string; follow?: boolean; className?: string }) {
  const { targets } = useTargets();
  const shown = targets.filter((x) => follow || x.id);
  const policies = shown.filter((x) => !x.node);
  const servers = shown.filter((x) => x.node);
  return (
    <Select id={id} className={className ?? "input max-w-[220px]"} value={value} onChange={(e) => onChange(e.target.value)} aria-label={label}>
      {policies.map((x) => (
        <option key={x.id} value={x.id}>
          {x.label}
        </option>
      ))}
      {servers.length ? (
        <optgroup label={t("settings.routesServer")}>
          {servers.map((x) => (
            <option key={x.id} value={x.id}>
              {x.label}
            </option>
          ))}
        </optgroup>
      ) : null}
    </Select>
  );
}
