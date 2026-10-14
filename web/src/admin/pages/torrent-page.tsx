import { PageHeader } from "../../components/ui";
import { t } from "../../i18n";
import { BackToAddons } from "./addon-row";
import { TorrentCard, TorrentHitsCard } from "./settings/torrent";

/** The torrent blocker, an addon of its own: its switch and its catches. */
export function TorrentPage() {
  return (
    <>
      <PageHeader title={t("settings.torrent.title")} sub={t("addons.torrentSub")} actions={<BackToAddons />} />
      <div className="flex max-w-4xl flex-col gap-4">
        <TorrentCard />
        <TorrentHitsCard />
      </div>
    </>
  );
}
