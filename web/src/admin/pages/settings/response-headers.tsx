import { Plus, X } from "lucide-react";
import type { Schemas } from "../../../api/client";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";

type Header = Schemas["Header"];

/**
 * More headers an answer of the subscription carries, a name and a value a row: a response
 * rule's, and a group's own over every answer. errorOf is the API's refusal of a row's part,
 * listError of the list.
 */
export function ResponseHeaders({
  headers,
  onChange,
  errorOf,
  listError,
  max,
}: {
  headers: Header[];
  onChange: (headers: Header[]) => void;
  errorOf: (i: number, part: "name" | "value") => string | undefined;
  listError?: string;
  max: number;
}) {
  const set = (i: number, patch: Partial<Header>) => onChange(headers.map((h, j) => (j === i ? { ...h, ...patch } : h)));
  return (
    <>
      {headers.length > 0 ? (
        <ul className="mb-2 flex flex-col gap-2">
          {headers.map((h, i) => {
            const error = errorOf(i, "name") ?? errorOf(i, "value");
            return (
              <li key={i} className="flex flex-wrap items-center gap-2">
                <input className="input mono w-[184px]" value={h.name} maxLength={64} spellCheck={false} autoCapitalize="off" placeholder="X-Header" aria-label={t("settings.subRules.header")} aria-invalid={!!errorOf(i, "name")} onChange={(e) => set(i, { name: e.target.value })} />
                <input className="input mono min-w-[160px] flex-1" value={h.value} maxLength={512} spellCheck={false} autoCapitalize="off" aria-label={t("settings.subRules.value")} aria-invalid={!!errorOf(i, "value")} onChange={(e) => set(i, { value: e.target.value })} />
                <Button size="sm" variant="ghost" aria-label={t("settings.subRules.removeHeader")} title={t("settings.subRules.removeHeader")} onClick={() => onChange(headers.filter((_, j) => j !== i))}>
                  <X size={16} aria-hidden />
                </Button>
                {error ? (
                  <p className="basis-full text-xs text-[var(--berry-600)]" role="alert">
                    {error}
                  </p>
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}
      {listError ? (
        <p className="mb-2 text-xs text-[var(--berry-600)]" role="alert">
          {listError}
        </p>
      ) : null}
      <Button size="sm" disabled={headers.length >= max} onClick={() => onChange([...headers, { name: "", value: "" }])}>
        <Plus size={16} aria-hidden /> {t("settings.subRules.addHeader")}
      </Button>
    </>
  );
}
