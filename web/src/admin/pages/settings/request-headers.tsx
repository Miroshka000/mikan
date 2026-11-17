import { Plus, X } from "lucide-react";
import { useId } from "react";
import type { Schemas } from "../../../api/client";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";
import { HeaderNames } from "./sub-rules-when";

type Header = Schemas["Header"];

// Headers a request most often differs by, one tap to add.
const QUICK = ["X-Device-Os", "X-Hwid", "Accept"] as const;

// The most headers a request to try carries: the API's bound.
const MAX = 30;

/**
 * The headers of a request to try besides its User-Agent: a name and a value a row, the ones
 * requests most often differ by a tap away. The rule tester and the preview of a client's
 * subscription ask with them.
 */
export function RequestHeaders({ headers, onChange }: { headers: Header[]; onChange: (headers: Header[]) => void }) {
  const names = useId();
  const set = (i: number, patch: Partial<Header>) => onChange(headers.map((h, j) => (j === i ? { ...h, ...patch } : h)));
  return (
    <>
      <HeaderNames id={names} />
      {headers.length > 0 ? (
        <ul className="mb-3 flex flex-col gap-2">
          {headers.map((h, i) => (
            <li key={i} className="flex flex-wrap items-center gap-2">
              <input className="input mono w-[168px]" list={names} value={h.name} maxLength={64} spellCheck={false} autoCapitalize="off" aria-label={t("settings.subRules.header")} onChange={(e) => set(i, { name: e.target.value })} />
              <input className="input mono min-w-[160px] flex-1" value={h.value} maxLength={512} spellCheck={false} autoCapitalize="off" aria-label={t("settings.subRules.value")} onChange={(e) => set(i, { value: e.target.value })} />
              <Button size="sm" variant="ghost" aria-label={t("settings.subRules.test.removeHeader")} title={t("settings.subRules.test.removeHeader")} onClick={() => onChange(headers.filter((_, j) => j !== i))}>
                <X size={16} aria-hidden />
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="mb-4 flex flex-wrap items-center gap-2" role="group" aria-label={t("settings.subRules.test.headers")}>
        <span className="text-xs text-[var(--ink-500)]">{t("settings.subRules.test.headers")}</span>
        {QUICK.map((name) => (
          <button key={name} type="button" className="chip-btn" disabled={headers.length >= MAX} onClick={() => onChange([...headers, { name, value: "" }])}>
            <Plus size={12} className="mr-1 inline" aria-hidden />
            <span className="mono">{name}</span>
          </button>
        ))}
      </div>
    </>
  );
}
