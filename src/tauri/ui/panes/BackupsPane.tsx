import { useEffect, useState } from 'react';

import type { Snapshot } from '../lib/contract';
import { exportBackup, forgetBackup, importBackup } from '../lib/desktop-actions';
import type { ServicePort } from '../lib/service';
import type { BackupPreviewFrame } from '../lib/wire';
import { Button, Group, Pane, PaneActions, Row, RowActions, RowText, WarningRow } from '../ui/primitives';
import { RowMenu } from '../ui/RowMenu';
import { CreateBackupSheet, RestoreBackupSheet } from './BackupSheets';

export function BackupsPane({
  service,
  snapshot,
  preview = null,
  busy = new Set<string>(),
  initialExportOpen = false,
  initialReviewOpen = false,
}: {
  service: ServicePort;
  snapshot: Snapshot;
  preview?: BackupPreviewFrame | null;
  busy?: ReadonlySet<string>;
  initialExportOpen?: boolean;
  initialReviewOpen?: boolean;
}) {
  const [exportOpen, setExportOpen] = useState(initialExportOpen);
  const [reviewOpen, setReviewOpen] = useState(() => initialReviewOpen || Boolean(preview && preview.status === 'ready'));
  const [lastReviewedPath, setLastReviewedPath] = useState('');

  useEffect(() => {
    if (preview && (preview.status === 'ready' || preview.status === 'error')) {
      setReviewOpen(true);
    }
  }, [preview]);

  const handleExport = (path: string, accountIds: string[], overwrite: boolean) => {
    const accounts = snapshot.accounts
      .filter((account) => accountIds.includes(account.id))
      .map((account) => ({ provider: account.providerId, name: account.name }));
    const action = exportBackup(path, accounts, overwrite);
    service.mutate(action.operation, action.extra);
    setExportOpen(false);
  };

  const handleQuickBackup = () => {
    service.mutate('account-backup-all', { path: '' });
    setExportOpen(false);
  };

  const handleReview = (path: string) => {
    setLastReviewedPath(path);
    service.inspectBackup(path);
  };

  const handleImport = (inspectionId: string, entryIds: string[], overwrite: boolean) => {
    const action = importBackup(inspectionId, entryIds, overwrite);
    service.mutate(action.operation, action.extra);
    setReviewOpen(false);
  };

  const handleDiscard = (inspectionId: string) => {
    const action = forgetBackup(inspectionId);
    service.mutate(action.operation, action.extra);
    setReviewOpen(false);
  };

  return (
    <Pane title="Backups" testId="backups">
      {/* Warning section matching macOS `BackupsView.swift:38-50` */}
      <Group testId="backups-warning">
        <WarningRow
          title="Backup files contain working credentials"
          detail="They are readable JSON and are not encrypted. Store them securely and don’t share them."
        />
      </Group>

      {/* Reviewed backups section matching macOS `BackupsView.swift:52-134` */}
      <Group
        title="Reviewed Backups"
        footer="Saved accounts only. API gateways, app settings, activity, and AnyPick settings are not included. For imported files, AnyPick stages a protected copy that is discarded when you forget the review."
      >
        {preview?.status === 'ready' ? (
          <Row testId={`backups.inventory.${preview.inspectionId || 'preview'}`}>
            <div className="flex h-7 w-7 items-center justify-center rounded text-accent">
              <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                <rect x="2" y="4" width="20" height="13" rx="2" />
                <path d="M6 13h.01M10 13h.01" />
                <path d="M15 8l2 2 4-4" />
                <line x1="2" y1="11" x2="22" y2="11" />
              </svg>
            </div>
            <RowText
              title={formatBackupDate(preview.createdAt)}
              detail={`${preview.entries.length} ${preview.entries.length === 1 ? 'account' : 'accounts'} · ${lastReviewedPath || 'Staged backup'}`}
            />
            <RowActions>
              <Button kind="secondary" onClick={() => setReviewOpen(true)}>
                Review
              </Button>
              <RowMenu label="More backup options" testId="backups.more-menu">
                {(close) => (
                  <>
                    <button
                      type="button"
                      role="menuitem"
                      className="flex w-full cursor-default items-center gap-2 rounded-control bg-transparent px-2 py-1 text-left text-[12px] hover:bg-hover"
                      onClick={() => {
                        close();
                        setReviewOpen(true);
                      }}
                    >
                      Review Backup…
                    </button>
                    {lastReviewedPath ? (
                      <button
                        type="button"
                        role="menuitem"
                        className="flex w-full cursor-default items-center gap-2 rounded-control bg-transparent px-2 py-1 text-left text-[12px] hover:bg-hover"
                        onClick={() => {
                          close();
                          void service.copy(lastReviewedPath);
                        }}
                      >
                        Copy File Path
                      </button>
                    ) : null}
                    <button
                      type="button"
                      role="menuitem"
                      className="flex w-full cursor-default items-center gap-2 rounded-control bg-transparent px-2 py-1 text-left text-[12px] text-red hover:bg-hover"
                      onClick={() => {
                        close();
                        if (preview.inspectionId) handleDiscard(preview.inspectionId);
                      }}
                    >
                      Forget Review…
                    </button>
                  </>
                )}
              </RowMenu>
              <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true" focusable="false" className="text-tertiary">
                <path d="M9 5l7 7-7 7" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
            </RowActions>
          </Row>
        ) : (
          <Row>
            <RowText detail="No reviewed backups. Choose a backup file to review it before importing." />
          </Row>
        )}
      </Group>

      {/* Quick backup section */}
      <Group
        title="Quick Backup"
        footer="Exports all accounts to a temporary file and reports that file's path. The file contains account credentials."
        testId="backups-create-quick"
      >
        <Row testId="backup-create-row">
          <RowText
            title="Create an account backup"
            detail="Save all saved accounts to a default timestamped file."
          />
          <RowActions>
            <Button
              kind="secondary"
              disabled={busy.size > 0}
              onClick={handleQuickBackup}
            >
              Create Backup
            </Button>
          </RowActions>
        </Row>
      </Group>

      {/* Bottom pane actions matching macOS `BackupsView.swift:204-216` */}
      <PaneActions>
        <Button
          kind="secondary"
          testId="backups-create"
          disabled={busy.size > 0 || snapshot.accounts.length === 0}
          onClick={() => setExportOpen(true)}
        >
          Export Accounts…
        </Button>
        <Button
          kind="secondary"
          testId="backups-import"
          disabled={busy.size > 0}
          onClick={() => setReviewOpen(true)}
        >
          Review Backup…
        </Button>
      </PaneActions>

      {/* Sheets matching macOS `CreateBackupSheet` & `RestoreBackupSheet` */}
      {exportOpen ? (
        <CreateBackupSheet
          accounts={snapshot.accounts}
          busy={busy.size > 0}
          onClose={() => setExportOpen(false)}
          onExport={handleExport}
        />
      ) : null}

      {reviewOpen ? (
        <RestoreBackupSheet
          preview={preview}
          busy={busy.size > 0}
          initialPath={lastReviewedPath}
          onClose={() => setReviewOpen(false)}
          onReview={handleReview}
          onImport={handleImport}
          onDiscard={handleDiscard}
        />
      ) : null}
    </Pane>
  );
}

function formatBackupDate(isoString?: string): string {
  if (!isoString) return 'Reviewed backup';
  try {
    const d = new Date(isoString);
    if (isNaN(d.getTime())) return isoString;
    return d.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
    });
  } catch {
    return isoString;
  }
}
