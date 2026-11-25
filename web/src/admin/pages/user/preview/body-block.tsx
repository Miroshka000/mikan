import { Copy, Download } from "lucide-react";
import { Button } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { useCopy } from "../../../../lib/copy";
import { saveText } from "../../../../lib/download";
import { bodyFile } from "./model";

/** The answer's body as the app gets it, with placeholder keys: copied or saved as a file. */
export function BodyBlock({ body, format, cut, name }: { body: string; format: string; cut: boolean; name: string }) {
  const copy = useCopy();
  const file = bodyFile(format);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={() => copy(body, t("userDrawer.preview.copied"))}>
          <Copy size={14} aria-hidden /> {t("userDrawer.preview.copy")}
        </Button>
        <Button size="sm" onClick={() => saveText(`${name}.${file.ext}`, body, file.type)}>
          <Download size={14} aria-hidden /> {t("userDrawer.preview.download")}
        </Button>
      </div>
      {cut ? <p className="text-xs text-[var(--honey-600)]">{t("userDrawer.preview.cut")}</p> : null}
      <pre className="panel-soft mono max-h-[420px] overflow-auto p-3 text-[12px] leading-[1.45]" tabIndex={0} aria-label={t("userDrawer.preview.body")}>
        {body}
      </pre>
    </div>
  );
}
