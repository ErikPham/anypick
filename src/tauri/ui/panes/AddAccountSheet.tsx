import { useEffect, useMemo, useRef, useState } from 'react';

import type { AccountProviderRow, LoginMethodRow } from '../lib/contract';
import {
  CLOUDFLARE_HINT,
  SIGN_OUT_MESSAGE,
  SOURCE_LABEL,
  accountIdentityFromDetection,
  availableSourceModes,
  blockerMessage,
  cloudflareRoutingComplete,
  cloudflareRoutingProfile,
  credentialInputCanSubmit,
  isZaiProvider,
  loginFailureMessage,
  loginFallbacks,
  needsCloudflareRouting,
  primaryTitle,
  savedAccountCanSubmit,
  selectedCredentialKind,
  selectedLocalLoginMethod,
  selectedProfileId,
  checkAgainHelp,
  identityDetail,
  identityTitle,
  isCommunityFlow,
  showsLocalSourcePicker,
  sourceAppName,
  sourceFooterFor,
  zcodeAppInstalled,
  zcodeHint,
  type SourceMode,
  type ZcodeContext,
} from '../lib/account-flow';
import type { ServicePort } from '../lib/service';
import type { ServiceResult } from '../lib/wire';
import { savePastedAuth } from '../lib/desktop-actions';
import { Dialog } from '../ui/Dialog';
import { MenuSelect } from '../ui/MenuSelect';
import { Field, Group, Row, RowActions, RowText, Select, StatusLabel } from '../ui/primitives';
import { Button, WarningGlyph } from '../ui/primitives';
import { SegmentedControl } from '../ui/SegmentedControl';

/** One command's answer, with the operation that produced it. */
export type AccountFlowResult = { operation: string; result: ServiceResult };

/**
 * `AddSavedAccountSheet`. One provider, one source, and the fields that source
 * needs.
 *
 * macOS picks the source first and the form follows it. The sheet this replaces
 * rendered a field dump per provider instead: a Save button, a Detect button, a
 * secret box, a kind picker and a name field all at once, for every provider, on
 * one scroll. That is a settings dump, not an Add Account flow, and it made the
 * primary action ambiguous — "Add Account" was the label on four different
 * commands.
 */
export function AddAccountSheet({
  providers,
  service,
  busy = false,
  flowResult,
  onSend,
  onClear,
  onClose,
}: {
  providers: readonly AccountProviderRow[];
  service: ServicePort;
  busy?: boolean;
  /** The engine's answer to the last command this sheet sent. */
  flowResult?: AccountFlowResult | null;
  onSend: (action: { operation: string; extra: Record<string, unknown> }) => void;
  onClear: (providerId: string, sourceId?: string) => void;
  onClose: () => void;
}) {
  const usable = providers.filter((provider) => provider.installed);
  const [providerId, setProviderId] = useState(usable[0]?.id ?? '');
  const provider = providers.find((row) => row.id === providerId);

  const modes = useMemo(() => availableSourceModes(provider), [provider]);
  const [mode, setMode] = useState<SourceMode>('thisMac');
  // The mode follows the provider. Switching to a provider with no browser
  // sign-in must not leave a "New Account" tab selected and silently disabled.
  const activeMode = modes.includes(mode) ? mode : (modes[0] as SourceMode);

  const [name, setName] = useState('');
  const [identity, setIdentity] = useState('');
  const [detectedSource, setDetectedSource] = useState('');
  const [methodId, setMethodId] = useState(provider?.loginMethods[0]?.id ?? '');
  const [localSourceId, setLocalSourceId] = useState(
    provider?.loginMethods.find((method) => method.sourceId)?.sourceId ?? '',
  );
  const [secret, setSecret] = useState('');
  const [revealSecret, setRevealSecret] = useState(false);
  const [kind, setKind] = useState(provider?.credentialKinds?.[0]?.kind ?? '');
  const [profile, setProfile] = useState(selectedProfileId(provider, kind, ''));
  const [routing, setRouting] = useState('');
  const [authJSON, setAuthJSON] = useState('');
  const [signOutOpen, setSignOutOpen] = useState(false);
  // The shell answers whether the ZCode app is on this Mac. Read once per open:
  // the answer cannot change while the sheet is up, and re-reading on every
  // render would ask the filesystem on every keystroke.
  const [zcode, setZcode] = useState<ZcodeContext | null>(null);
  useEffect(() => {
    if (!isZaiProvider(provider)) return;
    let live = true;
    void service.zcodeContext().then(
      (answer) => {
        if (live) setZcode(answer);
      },
      () => {
        // No answer means no plan context, which is a state the sheet already
        // knows how to word.
      },
    );
    return () => {
      live = false;
    };
  }, [provider, service]);
  // Set only by an `ACCOUNT_EXISTS` refusal, which is what makes Replace the
  // next command. Any other failure is not an invitation to overwrite: a
  // rejected secret or an unreadable document must not leave a button that
  // destroys the account it could not read.
  const [replaceExisting, setReplaceExisting] = useState(false);

  const method: LoginMethodRow | undefined =
    provider?.loginMethods.find((row) => row.id === methodId) ?? provider?.loginMethods[0];
  const localMethod = selectedLocalLoginMethod(provider, localSourceId);
  const credentialKind = selectedCredentialKind(provider, kind);
  const profileId = selectedProfileId(provider, kind, profile);

  const [pending, setPending] = useState('');
  const [stagedPath, setStagedPath] = useState('');
  // One row for both the engine's refusal and its advice. macOS keeps them in
  // one place because the advice only means anything next to the refusal.
  const [failure, setFailure] = useState<{ message: string; title: string } | null>(null);
  const stagedRef = useRef('');
  stagedRef.current = stagedPath;

  // A staged document is this sheet's own temp file. Leaving the sheet with one
  // staged would leave JSON in the temp directory until the OS cleaned it up.
  useEffect(() => {
    return () => {
      if (stagedRef.current) void service.forgetAuthPaste(stagedRef.current);
    };
  }, [service]);

  // Every answer is matched to the command it answers. An answer for a request
  // this sheet never sent — a backup import on another pane, say — must not
  // repopulate a field here.
  const seenResult = useRef('');
  useEffect(() => {
    if (!flowResult || flowResult.result.requestId === seenResult.current) return;
    seenResult.current = flowResult.result.requestId;
    const { operation, result } = flowResult;
    if (operation === 'account-detect') {
      setPending('');
      if (result.status === 'success') {
        setDetectedSource(detectedSourceFrom(result.message));
        setIdentity(accountIdentityFromDetection(result.message) ?? 'Current login');
        setFailure(null);
        return;
      }
      setFailure({ title: 'No login found', message: loginFailureMessage(result.message) });
      return;
    }
    if (operation === 'account-save-auth') {
      setPending('');
      if (result.status === 'success') {
        setFailure(null);
        return;
      }
      // The staged file is still the one the engine refused, so Replace can send
      // the same document without staging a second copy.
      setReplaceExisting(result.code === 'ACCOUNT_EXISTS');
      setFailure({
        title: result.code === 'ACCOUNT_EXISTS' ? 'This account already exists' : 'Couldn’t save that file',
        message: loginFailureMessage(result.message),
      });
      return;
    }
    setPending('');
    if (result.status === 'error') {
      setFailure({ title: 'Unable to Complete', message: loginFailureMessage(result.message) });
      return;
    }
    setFailure(null);
  }, [flowResult]);

  const detecting = pending === 'account-detect';
  const blocked = busy || pending !== '';
  const hasIdentity = identity.trim() !== '';
  const blocker = blockerMessage({ provider, method, isNewAccountMode: activeMode === 'newAccount' });
  const routingComplete = !needsCloudflareRouting(provider) || cloudflareRoutingComplete(routing);

  const canSubmit =
    activeMode === 'credential'
      ? credentialInputCanSubmit({
          name,
          secret,
          providerInstalled: provider?.installed ?? false,
          blocked,
          routingComplete,
        })
      : activeMode === 'paste'
        ? // A pasted document is the whole account; the name is optional and the
          // engine's own collision message is what offers Replace.
          authJSON.trim() !== '' && !blocked
        : savedAccountCanSubmit({
            name,
            isNewAccountMode: activeMode === 'newAccount',
            hasIdentity,
            hasLiveLogin: Boolean(provider?.livePresent),
            methodAvailable: method?.available ?? false,
            providerInstalled: provider?.installed ?? false,
            blocked,
          });

  const send = (operation: string, extra: Record<string, unknown>) => {
    setPending(operation);
    setFailure(null);
    onSend({ operation, extra });
  };

  const submit = () => {
    if (!provider) return;
    const trimmed = name.trim();
    if (activeMode === 'credential') {
      send('account-save-input', {
        providerId: provider.providerId,
        name: trimmed,
        kind: credentialKind?.kind ?? kind,
        profile: needsCloudflareRouting(provider) ? cloudflareRoutingProfile(routing) : profileId,
        // The secret travels in `apiKey`, the same field `gateway-create` uses
        // for a profile secret, so the protocol has one secret-shaped field.
        apiKey: secret.trim(),
      });
      return;
    }
    if (activeMode === 'newAccount') {
      send('account-login', {
        providerId: provider.providerId,
        name: trimmed,
        methodId: method?.id ?? '',
      });
      return;
    }
    if (activeMode === 'paste') {
      void savePaste(trimmed);
      return;
    }
    send('account-save', {
      providerId: provider.providerId,
      name: trimmed,
      // macOS substitutes a readable sentence for a login it could not
      // identify, so the saved snapshot always names something.
      identity: identity || `Current ${provider.label} login`,
      sourceId: localMethod?.sourceId || undefined,
    });
  };

  /**
   * Stage the document, then save it.
   *
   * The document never goes on the mutate line — a Codex `auth.json` with
   * several JWTs does not fit in `MaxCommandBytes` — so the host writes a
   * private temp file and the engine reads a path. Staging is what keeps the
   * secret out of a CI log (ADR 0006).
   */
  async function savePaste(accountName: string) {
    if (!provider) return;
    const body = authJSON.trim();
    if (!body) return;
    setPending('account-save-auth');
    setFailure(null);
    let path = stagedPath;
    // A refusal leaves the staged file in place so Replace can reuse it; a fresh
    // edit invalidates it.
    const overwrite = replaceExisting;
    if (!overwrite) {
      try {
        if (path) void service.forgetAuthPaste(path);
        path = await service.stageAuthPaste(body);
      } catch (thrown) {
        setStagedPath('');
        setPending('');
        setReplaceExisting(false);
        setFailure({ title: 'Couldn’t read that file', message: pasteError(thrown) });
        return;
      }
      setStagedPath(path);
    }
    onSend(
      savePastedAuth({
        providerId: provider.providerId,
        name: accountName || undefined,
        path,
        overwrite,
      }),
    );
  }

  const detect = () => {
    if (!provider) return;
    send('account-detect', {
      providerId: provider.providerId,
      sourceId: localMethod?.sourceId ?? provider.sourceId,
    });
  };

  const appName = sourceAppName({ provider, localMethod });
  const checking = detecting || (activeMode === 'newAccount' && pending === 'account-login');

  const fallbacks = loginFallbacks({
    isNewAccountMode: activeMode === 'newAccount',
    hasLiveLogin: Boolean(provider?.livePresent),
    hasCredentialKinds: (provider?.credentialKinds?.length ?? 0) > 0,
  });

  return (
    <Dialog
      title="Add Account"
      onCancel={onClose}
      testId="account-flow.provider"
      width="form"
      actions={
        <>
          <Button kind="quiet" onClick={onClose}>
            Cancel
          </Button>
          <Button kind="primary" testId="account-flow.save" disabled={!canSubmit} onClick={submit}>
            {primaryTitle({
              mode: activeMode,
              providerLabel: provider?.label ?? '',
              accountName: name,
              isNewAccount: activeMode === 'newAccount',
              hasIdentity,
              blocked,
              replacing: replaceExisting,
            })}
          </Button>
        </>
      }
    >
      <Group
        footer={sourceFooterFor({
          mode: activeMode,
          providerLabel: provider?.label ?? '',
          appName,
          isChecking: checking,
          isNewAccountMode: activeMode === 'newAccount',
          hasIdentity,
        })}
      >
        {modes.length > 1 ? (
          <Row testId="account-flow.source">
            <SegmentedControl
              className="w-full"
              value={activeMode}
              testId="account-flow.source.picker"
              onChange={(next) => setMode(next as SourceMode)}
              options={modes.map((mode) => ({ value: mode, label: SOURCE_LABEL[mode] }))}
            />
          </Row>
        ) : null}

        <Row testId={`account-flow.provider-group.${providerId}`}>
          <RowText title="Provider" detail={provider?.detail} />
          <RowActions>
            <MenuSelect
              label=""
              ariaLabel={`Provider for ${provider?.label ?? 'the new account'}`}
              value={providerId}
              testId="account-flow.provider.picker"
              onChange={(next) => {
                setProviderId(next);
                const nextProvider = providers.find((row) => row.id === next);
                setMode((availableSourceModes(nextProvider)[0] as SourceMode) ?? 'thisMac');
                setMethodId(nextProvider?.loginMethods[0]?.id ?? '');
                setKind(nextProvider?.credentialKinds?.[0]?.kind ?? '');
                setProfile('');
                setIdentity('');
                setStagedPath('');
                setReplaceExisting(false);
                setFailure(null);
              }}
              options={providers.map((row) => ({
                value: row.id,
                label: row.label,
                provider: row.id,
              }))}
            />
          </RowActions>
        </Row>

        {showsLocalSourcePicker(provider) && activeMode === 'thisMac' ? (
          <Row testId="account-flow.local-source">
            <RowText title="App" detail="Choose which local app's login AnyPick should read." />
            <RowActions>
              <Select
                label=""
                ariaLabel="Local app"
                value={localMethod?.sourceId ?? ''}
                testId="account-flow.local-source.picker"
                onChange={setLocalSourceId}
                options={(provider?.loginMethods ?? [])
                  .filter((row) => row.sourceId)
                  .map((row) => ({ value: row.sourceId as string, label: row.sourceLabel ?? row.label }))}
              />
            </RowActions>
          </Row>
        ) : null}

        {activeMode === 'paste' ? (
          <Row testId="account-flow.auth-json">
            <div className="flex min-w-0 flex-1 flex-col gap-1.5 py-1">
              <textarea
                className="min-h-[7.5rem] w-full rounded-control border border-control-border bg-field p-2 font-mono text-[12px] text-text placeholder:text-tertiary focus:border-accent"
                placeholder="Paste auth JSON here…"
                value={authJSON}
                spellCheck={false}
                data-testid="account-flow.auth-json.input"
                onChange={(event) => {
                  setAuthJSON(event.target.value);
                  setReplaceExisting(false);
                  setFailure(null);
                  if (stagedPath) {
                    void service.forgetAuthPaste(stagedPath);
                    setStagedPath('');
                  }
                }}
              />
              <p className="text-[11px] text-secondary">
                Paste the provider&rsquo;s own auth file. This does not change the login already on this Mac.
              </p>
            </div>
          </Row>
        ) : activeMode === 'credential' ? (
          <div className="contents" data-testid="account-flow.api-key">
            {(provider?.credentialKinds?.length ?? 0) > 1 ? (
              <Row testId="account-flow.credential-kind">
                <RowText title="Credential" />
                <RowActions>
                  <Select
                    label=""
                    ariaLabel="Credential kind"
                    value={credentialKind?.kind ?? ''}
                    testId="account-flow.credential-kind.picker"
                    onChange={(next) => {
                      setKind(next);
                      setProfile('');
                    }}
                    options={(provider?.credentialKinds ?? []).map((row) => ({
                      value: row.kind,
                      label: row.label,
                    }))}
                  />
                </RowActions>
              </Row>
            ) : null}

            {needsCloudflareRouting(provider) ? (
              <>
                <Row testId="account-flow.cloudflare-routing">
                  <Field
                    label="Account / Gateway"
                    value={routing}
                    placeholder="<account_id>/<gateway_id>"
                    onChange={setRouting}
                    testId="account-flow.cloudflare-routing.input"
                  />
                </Row>
                <Row>
                  <RowText detail={CLOUDFLARE_HINT} testId="account-flow.cloudflare-hint" />
                </Row>
              </>
            ) : null}

            <Row testId="account-flow.secret">
              <Field
                label={credentialKind?.label ?? 'API Key'}
                value={secret}
                onChange={setSecret}
                type={revealSecret ? 'text' : 'password'}
                testId="account-flow.api-key.input"
              />
              <RowActions>
                <Button
                  kind="secondary"
                  testId="account-flow.api-key.reveal"
                  onClick={() => setRevealSecret((current) => !current)}
                >
                  {revealSecret ? 'Hide' : 'Show'}
                </Button>
              </RowActions>
            </Row>

            {(credentialKind?.profiles.length ?? 0) > 1 && !needsCloudflareRouting(provider) ? (
              <Row testId="account-flow.credential-profile">
                <RowText title="Profile" />
                <RowActions>
                  <Select
                    label=""
                    ariaLabel="Profile"
                    value={profileId}
                    testId="account-flow.credential-profile.picker"
                    onChange={setProfile}
                    options={(credentialKind?.profiles ?? []).map((row) => ({
                      value: row.id,
                      label: row.label,
                    }))}
                  />
                </RowActions>
              </Row>
            ) : null}

            {isZaiProvider(provider) ? (
              <Row>
                <RowText detail={zcodeHint(zcode?.planWarning)} testId="account-flow.zcode-hint" />
              </Row>
            ) : null}
          </div>
        ) : (
          <>
            <Row testId="account-flow.identity">
              <RowText
                title={identityTitle({
                  identity,
                  appName,
                  isChecking: checking,
                  isNewAccountMode: activeMode === 'newAccount',
                })}
                detail={identityDetail({
                  identity,
                  detectedSource,
                  appName,
                  isChecking: checking,
                  isNewAccountMode: activeMode === 'newAccount',
                  providerLabel: provider?.label ?? '',
                  isCommunityFlow: isCommunityFlow(method),
                  zcodeAppInstalled: zcodeAppInstalled({ provider, context: zcode ?? undefined }),
                })}
                issue={provider?.livePresent && !identity && !checking ? 'Not checked yet.' : undefined}
              />
              <RowActions>
                {provider?.livePresent && identity ? <StatusLabel status="ready" /> : null}
                <Button
                  kind="secondary"
                  disabled={blocked}
                  testId="account-flow.detect"
                  title={checkAgainHelp({
                    appName,
                    isNewAccountMode: activeMode === 'newAccount',
                    hasIdentity,
                    providerLabel: provider?.label ?? '',
                    isCommunityFlow: isCommunityFlow(method),
                  })}
                  onClick={detect}
                >
                  {identity || provider?.liveIdentity ? 'Check Again' : 'Detect'}
                </Button>
                {provider?.canClear && provider?.livePresent && activeMode === 'thisMac' ? (
                  <Button
                    kind="secondary"
                    disabled={blocked}
                    testId="account-flow.clear"
                    onClick={() => setSignOutOpen(true)}
                  >
                    Sign Out…
                  </Button>
                ) : null}
              </RowActions>
            </Row>

            {activeMode === 'newAccount' && (provider?.loginMethods.length ?? 0) > 1 ? (
              <Row testId="account-flow.method">
                <RowText title="Sign-in method" detail={method?.detail} />
                <RowActions>
                  <Select
                    label=""
                    ariaLabel="Sign-in method"
                    value={method?.id ?? ''}
                    testId="account-flow.method.picker"
                    onChange={setMethodId}
                    options={(provider?.loginMethods ?? []).map((row) => ({ value: row.id, label: row.label }))}
                  />
                </RowActions>
              </Row>
            ) : null}

            {blocker ? (
              <Row testId="account-flow.blocker" warning>
                <RowText title="Can’t add this account" issue={blocker} />
              </Row>
            ) : null}
          </>
        )}

        <Row testId="account-flow.name-group">
          <Field
            label="Account Name"
            value={name}
            placeholder={activeMode === 'paste' ? 'Optional' : 'Personal'}
            onChange={setName}
            testId="account-flow.name"
          />
        </Row>

        {failure ? (
          <Row testId="account-flow.error" warning>
            <WarningGlyph />
            <div className="flex min-w-0 flex-1 flex-col gap-row-stack">
              <div className="text-[12px] font-semibold text-text">{failure.title}</div>
              <div className="text-[12px] leading-[1.3] text-secondary">{failure.message}</div>
              {fallbacks.currentLogin ? (
                <RowActions>
                  <Button
                    kind="secondary"
                    testId="account-flow.error.current-login"
                    onClick={() => {
                      setMode('thisMac');
                      setFailure(null);
                      detect();
                    }}
                  >
                    Use Account on This Mac
                  </Button>
                </RowActions>
              ) : null}
              {fallbacks.credential ? (
                <RowActions>
                  <Button
                    kind="secondary"
                    testId="account-flow.error.credential"
                    onClick={() => {
                      setMode('credential');
                      setKind(provider?.credentialKinds?.[0]?.kind ?? '');
                      setProfile('');
                      setFailure(null);
                    }}
                  >
                    Use API Key
                  </Button>
                </RowActions>
              ) : null}
            </div>
          </Row>
        ) : null}
      </Group>

      {signOutOpen ? (
        <Dialog
          title="Sign Out?"
          message={SIGN_OUT_MESSAGE}
          onCancel={() => setSignOutOpen(false)}
          testId="account-flow.sign-out.dialog"
          actions={
            <>
              <Button kind="quiet" onClick={() => setSignOutOpen(false)}>
                Cancel
              </Button>
              <Button
                kind="destructive"
                testId="account-flow.sign-out"
                onClick={() => {
                  if (!provider) return;
                  setSignOutOpen(false);
                  setIdentity('');
                  setDetectedSource('');
                  onClear(provider.providerId, localMethod?.sourceId ?? provider.sourceId);
                }}
              >
                Sign Out
              </Button>
            </>
          }
        />
      ) : null}
    </Dialog>
  );
}

function pasteError(error: unknown): string {
  if (typeof error === 'string' && error) return error;
  if (error instanceof Error && error.message) return error.message;
  return 'AnyPick could not store the pasted auth JSON.';
}

/** Reads the identity out of the engine's detection sentence. */
export { accountIdentityFromDetection };

/**
 * The app the engine found the login in.
 *
 * The engine's sentence is "Detected <identity> in <source>." and the source
 * is the half after the *last* " in ", which is why the identity parse and this
 * one cannot share a split on the first occurrence.
 */
function detectedSourceFrom(message: string | undefined): string {
  if (!message?.startsWith('Detected ')) return '';
  const body = message.slice('Detected '.length);
  const marker = body.lastIndexOf(' in ');
  if (marker < 0) return '';
  return body.slice(marker + ' in '.length).replace(/\.$/, '').trim();
}