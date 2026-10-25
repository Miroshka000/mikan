import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import type { CSSProperties } from "react";
import { CardHead } from "../../../components/layout";
import { t } from "../../../i18n";

// The webhooks have a page of their own: they are for integrations, not for daily work.
export function WebhooksCard() {
  return (
    <section className="card glass reveal" style={{ "--i": 6 } as CSSProperties}>
      <CardHead
        title={t("webhooks.linkTitle")}
        sub={t("webhooks.linkSub")}
        aside={
          <Link to="/settings/webhooks" className="btn btn-glass btn-sm">
            {t("webhooks.linkOpen")} <ChevronRight size={16} aria-hidden />
          </Link>
        }
      />
    </section>
  );
}
