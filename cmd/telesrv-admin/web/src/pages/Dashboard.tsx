import { CheckCircle2, ChevronRight, Clock3, FileJson, KeyRound, MessageSquareText, ShieldCheck, Users } from "lucide-react";
import type { ReactNode } from "react";
import { AppLink } from "../components/AppLink";
import { StatusItem } from "../components/ui";
import { useI18n } from "../i18n";
import type { Navigate } from "../routing";

export function Dashboard({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  return (
    <div className="dashboard-layout">
      <section className="overview-band">
        <div>
          <div className="eyebrow">{t("dashboard.eyebrow")}</div>
          <h2>{t("dashboard.title")}</h2>
        </div>
        <div className="overview-metrics">
          <StatusItem label={t("dashboard.readPath")} value={t("dashboard.readPathValue")} tone="neutral" />
          <StatusItem label={t("dashboard.writePath")} value="Admin API" tone="good" />
          <StatusItem label={t("dashboard.executionPolicy")} value={t("dashboard.dryRunFirst")} tone="warn" />
        </div>
      </section>
      <div className="command-grid">
        <Launcher icon={<Users />} title={t("route.accounts")} text={t("dashboard.accountsText")} href="/accounts" navigate={navigate} />
        <Launcher icon={<ShieldCheck />} title={t("route.channels")} text={t("dashboard.channelsText")} href="/channels" navigate={navigate} />
        <Launcher icon={<MessageSquareText />} title={t("route.messages")} text={t("dashboard.messagesText")} href="/messages" navigate={navigate} />
      </div>
      <section className="work-strip">
        <div className="strip-item"><CheckCircle2 size={16} /><span>{t("dashboard.strip.dryRun")}</span></div>
        <div className="strip-item"><KeyRound size={16} /><span>{t("dashboard.strip.token")}</span></div>
        <div className="strip-item"><Clock3 size={16} /><span>{t("dashboard.strip.pagination")}</span></div>
        <div className="strip-item"><FileJson size={16} /><span>{t("dashboard.strip.snapshot")}</span></div>
      </section>
    </div>
  );
}

function Launcher({
  icon,
  title,
  text,
  href,
  navigate
}: {
  icon: ReactNode;
  title: string;
  text: string;
  href: string;
  navigate: Navigate;
}) {
  return (
    <AppLink className="launcher" href={href} navigate={navigate}>
      <span className="launcher-icon">{icon}</span>
      <span className="launcher-copy">
        <strong>{title}</strong>
        <span>{text}</span>
      </span>
      <ChevronRight size={16} />
    </AppLink>
  );
}
