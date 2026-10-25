import { useState } from "react";
import { CommandFlow } from "../components/CommandFlow";
import { useI18n } from "../i18n";
import type { CommandResult } from "../types";

type PackOutcome = { title: string; status: string; gift_id?: string; error?: string };

// GiftPackModal imports a .zip of gift definitions. It runs on CommandFlow, the
// same audited reason -> dry-run -> confirm sequence every other command in the
// panel uses, because it is one: the import is a CommandResult with an actor and
// a reason like any other. It used to carry its own copy of that flow, which is
// how its audit reason and step handling could drift from the rest of the panel.
//
// The archive goes up as multipart; the reason, the dry-run flag and the
// command id ride in the `metadata` field the endpoint parses.
export function GiftPackModal({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [outcome, setOutcome] = useState<CommandResult | null>(null);

  const gifts = (outcome?.details?.gifts ?? []) as PackOutcome[];

  return (
    <CommandFlow
      path="/api/actions/import-gift-pack"
      label={t("gifts.pack.title")}
      eyebrow={t("gifts.importEyebrow")}
      onApplied={onImported}
      onResult={setOutcome}
      onClose={onClose}
      disabled={!file}
      // A file cannot go in the JSON preview, so it is named there instead --
      // the operator still sees which archive is about to be imported.
      payload={() => ({ file: file ? file.name : null, bytes: file?.size ?? 0 })}
      form={(fields) => {
        const data = new FormData();
        if (file) data.append("file", file);
        data.append("metadata", JSON.stringify(fields));
        return data;
      }}
      extra={
        <>
          <p>{t("gifts.pack.hint")}</p>
          <label>
            <span>{t("gifts.pack.file")}</span>
            <input
              type="file"
              accept=".zip,application/zip"
              onChange={(event) => {
                setFile(event.target.files?.[0] ?? null);
                setOutcome(null);
              }}
            />
          </label>
          {file && <div className="muted">{file.name} · {(file.size / 1048576).toFixed(1)} MB</div>}
          {outcome && (
            <div className="gift-validation">
              <strong>
                {outcome.dry_run ? t("gifts.pack.preview") : t("gifts.pack.result")}:{" "}
                {String(outcome.details?.pack_name ?? "")}
              </strong>
              <div className="mono">SHA-256: {String(outcome.details?.content_sha256 ?? "")}</div>
              <ul>
                {gifts.map((gift, index) => (
                  <li key={`${gift.title}-${index}`}>
                    <strong>{gift.title}</strong> — {t(`gifts.pack.${gift.status}`)}{" "}
                    {gift.gift_id ? `#${gift.gift_id}` : ""}
                    {gift.error ? `: ${gift.error}` : ""}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      }
    />
  );
}