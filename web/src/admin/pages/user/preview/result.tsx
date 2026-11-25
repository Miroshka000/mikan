import { useId, type ReactNode } from "react";
import type { User } from "../../../../api/client";
import { t } from "../../../../i18n";
import { CoreNotes } from "../../settings/core-notes";
import { BodyBlock } from "./body-block";
import { HeaderList } from "./header-list";
import { LeftList } from "./left-list";
import type { Preview } from "./model";
import { ProxyList } from "./proxy-list";
import { PreviewSummary } from "./summary";

/**
 * What the app of the request gets: the answer in short, its servers and what of the panel's
 * is not among them, the headers, what the format's core makes of the profile its own way and
 * the answer itself.
 */
export function PreviewResult({ p, u }: { p: Preview; u: User }) {
  // A refused or dropped request gets no servers, nor does the subscription page: what they
  // would be tells nothing.
  const gave = (p.outcome === "served" || p.outcome === "stub") && p.format !== "html";
  return (
    <div className="mt-4 flex flex-col gap-4">
      <PreviewSummary p={p} u={u} />
      {gave ? (
        <Part title={t("userDrawer.preview.proxies", { n: p.proxies.length })}>
          <ProxyList proxies={p.proxies} />
        </Part>
      ) : null}
      {gave ? (
        <Part title={t("userDrawer.preview.leftTitle")}>
          <LeftList left={p.left} />
        </Part>
      ) : null}
      {p.headers.length > 0 ? (
        <Part title={t("userDrawer.preview.headers")}>
          <HeaderList headers={p.headers} />
        </Part>
      ) : null}
      {p.notes.length > 0 ? (
        <Part title={t("settings.routesNotesTitle")} sub={t("settings.routesNotesSub")}>
          <CoreNotes notes={p.notes} />
        </Part>
      ) : null}
      {p.body ? (
        <Part title={t("userDrawer.preview.body")} sub={t("userDrawer.preview.bodySub")}>
          <BodyBlock body={p.body} format={p.format} cut={!!p.body_cut} name={`subscription-${u.id}`} />
        </Part>
      ) : p.format === "html" ? (
        <p className="text-xs text-[var(--ink-500)]">{t("userDrawer.preview.htmlNote")}</p>
      ) : null}
    </div>
  );
}

function Part({ title, sub, children }: { title: string; sub?: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id}>
      <h4 id={id} className="text-[13px] font-semibold text-[var(--ink-700)]">
        {title}
      </h4>
      {sub ? <p className="mt-1 text-xs text-[var(--ink-500)]">{sub}</p> : null}
      <div className="mt-2">{children}</div>
    </section>
  );
}
