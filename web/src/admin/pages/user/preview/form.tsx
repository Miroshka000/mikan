import { useId, useState, type FormEvent } from "react";
import type { Schemas } from "../../../../api/client";
import { Select } from "../../../../components/select";
import { Button, Field } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { appName } from "../../../../lib/format";
import { namedHeaders } from "../../../../lib/sub-rules";
import { RequestHeaders } from "../../settings/request-headers";
import { PREVIEW_FORMATS, type PreviewFormat, type PreviewRequest } from "./model";

/**
 * The request a preview answers: the app's User-Agent (one tap for the apps of the client's
 * own latest requests), the other headers that matter and the format a link may name.
 */
export function PreviewForm({ agents, busy, onSubmit }: { agents: string[]; busy: boolean; onSubmit: (body: PreviewRequest) => void }) {
  const uid = useId();
  // Until the admin types or picks one, the client's latest app: the log may load after the form.
  const [typed, setAgent] = useState<string | null>(null);
  const agent = typed ?? agents[0] ?? "";
  const [headers, setHeaders] = useState<Schemas["Header"][]>([]);
  const [format, setFormat] = useState<"" | PreviewFormat>("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit({ user_agent: agent.trim(), headers: namedHeaders(headers), ...(format ? { format } : {}) });
  };
  return (
    <form onSubmit={submit} noValidate>
      <Field label={t("userDrawer.preview.agent")} htmlFor={`${uid}-agent`} hint={t("userDrawer.preview.agentHint")}>
        <input
          id={`${uid}-agent`}
          className="input mono"
          value={agent}
          maxLength={1024}
          spellCheck={false}
          autoCapitalize="off"
          autoComplete="off"
          placeholder="Happ/3.6.0 (iOS 18.1)"
          onChange={(e) => setAgent(e.target.value)}
        />
      </Field>
      {agents.length > 0 ? (
        <div className="-mt-1 mb-4 flex flex-wrap items-center gap-2" role="group" aria-label={t("userDrawer.preview.recent")}>
          <span className="text-xs text-[var(--ink-500)]">{t("userDrawer.preview.recent")}</span>
          {agents.map((a) => (
            <button key={a} type="button" className="chip-btn max-w-[220px] truncate" aria-pressed={agent.trim() === a} title={a} onClick={() => setAgent(a)}>
              {appName(a) || a}
            </button>
          ))}
        </div>
      ) : null}
      <RequestHeaders headers={headers} onChange={setHeaders} />
      <Field label={t("userDrawer.preview.format")} htmlFor={`${uid}-format`} hint={t("userDrawer.preview.formatHint")}>
        <Select id={`${uid}-format`} className="input max-w-[320px]" value={format} onChange={(e) => setFormat(e.target.value as "" | PreviewFormat)}>
          {PREVIEW_FORMATS.map((f) => (
            <option key={f} value={f}>
              {t(`userDrawer.preview.formats.${f || "auto"}`)}
            </option>
          ))}
        </Select>
      </Field>
      <Button type="submit" variant="primary" loading={busy}>
        {t("userDrawer.preview.run")}
      </Button>
    </form>
  );
}
