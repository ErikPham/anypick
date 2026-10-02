import { useState } from 'react';

import type { CodexProfileRow, Snapshot } from '../lib/contract';
import { cleanModelDisplayName } from '../lib/model';
import { validCodexProfileName } from '../lib/desktop-actions';
import { Dialog } from '../ui/Dialog';
import { Button, Field, Group, Row, Select } from '../ui/primitives';

/**
 * CodexProfileSheet: modal dialog to Add or Edit a Codex Profile.
 * Mirrors macOS `CodexProfileSheet` in `src/macos/AppSheets.swift:793-920`.
 */
export function CodexProfileSheet({
  profile,
  sources,
  existingProfiles,
  busy,
  onClose,
  onSave,
}: {
  profile?: CodexProfileRow | null;
  sources: Snapshot['codexProfileSources'];
  existingProfiles: Snapshot['codexProfiles'];
  busy: ReadonlySet<string>;
  onClose: () => void;
  onSave: (payload: {
    name: string;
    sourceId: string;
    modelId: string;
    transportPolicy: string;
    isUpdate: boolean;
  }) => void;
}) {
  const isEdit = Boolean(profile);
  const [name, setName] = useState(profile?.name ?? '');
  const [sourceId, setSourceId] = useState(profile?.sourceId ?? sources[0]?.sourceId ?? '');
  const [modelId, setModelId] = useState(profile?.modelId ?? '');
  const [transport, setTransport] = useState(profile?.transportPolicy || 'auto');

  const selectedSource = sources.find((s) => s.sourceId === sourceId);
  const availableModels = selectedSource?.models ?? [];

  const nameAlreadyExists =
    !isEdit && existingProfiles.some((p) => p.name.toLowerCase() === name.trim().toLowerCase());
  const invalidName = name.length > 0 && !validCodexProfileName(name);
  const canSave =
    validCodexProfileName(name) &&
    !nameAlreadyExists &&
    Boolean(selectedSource) &&
    busy.size === 0;

  return (
    <Dialog
      title={isEdit ? 'Edit Codex Profile' : 'Add Codex Profile'}
      onCancel={onClose}
      testId="codex-profile-dialog"
      width="form"
      actions={
        <>
          <Button kind="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            kind="primary"
            disabled={!canSave}
            testId="codex-profile-save"
            onClick={() => {
              onSave({
                name,
                sourceId,
                modelId,
                transportPolicy: transport,
                isUpdate: isEdit,
              });
            }}
          >
            {isEdit ? 'Save Profile' : 'Add Profile'}
          </Button>
        </>
      }
    >
      <Group title="Profile" footer={isEdit ? 'A profile’s name can’t be changed after creation.' : undefined}>
        <Row>
          {isEdit ? (
            <div className="flex min-w-0 flex-1 items-center justify-between py-1">
              <span className="text-[13px] text-text">Name</span>
              <span className="text-[13px] font-medium text-text">{name}</span>
            </div>
          ) : (
            <div className="flex w-full flex-col gap-1">
              <Field
                label="Name"
                value={name}
                placeholder="e.g. work"
                onChange={setName}
                testId="codex-profile-name"
              />
              <span className="text-[11px] text-secondary">
                Use 1–64 letters, numbers, hyphens, or underscores.
              </span>
              {invalidName ? (
                <span className="text-[11px] text-red">Invalid profile name format.</span>
              ) : nameAlreadyExists ? (
                <span className="text-[11px] text-red">That profile name is already in use.</span>
              ) : null}
            </div>
          )}
        </Row>
      </Group>

      <Group title="Connection">
        <Row>
          <Select
            label="Account or Route"
            value={sourceId}
            options={sources.map((s) => ({
              value: s.sourceId,
              label: s.label + (s.detail ? ` (${s.detail})` : ''),
            }))}
            ariaLabel="Codex route"
            testId="codex-profile.source"
            disabled={busy.size > 0}
            onChange={(nextSource) => {
              setSourceId(nextSource);
              setModelId('');
            }}
          />
        </Row>
      </Group>

      <Group title="Transport Policy">
        <Row>
          <Select
            label="Transport"
            value={transport}
            options={[
              { value: 'auto', label: 'Automatic' },
              { value: 'proxy', label: 'Local AnyPick proxy' },
              { value: 'direct', label: 'Direct vendor connection' },
            ]}
            ariaLabel="Codex transport"
            testId="codex-profile.transport"
            disabled={busy.size > 0}
            onChange={setTransport}
          />
        </Row>
      </Group>

      <Group title="Model">
        <Row>
          <Select
            label="Model"
            value={modelId}
            options={[
              { value: '', label: 'Source default' },
              ...availableModels.map((m) => ({ value: m, label: cleanModelDisplayName(m) })),
            ]}
            ariaLabel="Codex model"
            testId="codex-profile.model"
            disabled={busy.size > 0}
            onChange={setModelId}
          />
        </Row>
      </Group>
    </Dialog>
  );
}
