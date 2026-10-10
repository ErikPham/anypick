import { useEffect, useMemo, useState } from 'react';

import type { AccountRow, GatewayProviderRow } from '../lib/contract';
import {
  TEST_LABEL,
  TEST_STATE_LABEL,
  connectionFooter,
  connectionTestEnabled,
  gatewayCanSave,
  isApiKeyAccount,
  providerSuppliesEndpoint,
  requiresApiKey,
  resolvedEndpoint,
  suggestedGatewayName,
  syncRegion,
  usesCustomEndpoint,
  type ConnectionState,
} from '../lib/gateway-flow';
import type { ServicePort } from '../lib/service';
import type { ServiceResult } from '../lib/wire';
import { Dialog } from '../ui/Dialog';
import { MenuSelect } from '../ui/MenuSelect';
import { Button, Field, Group, Row, RowActions, RowText, Select, StatusLabel } from '../ui/primitives';

/** The answer to a `gateway-test` the sheet asked for. */
export type GatewayFlowResult = { operation: string; result: ServiceResult };

/**
 * `AddGatewaySheet`. A gateway is a provider key plus an endpoint, so it gets
 * its own sheet rather than a block inside Add Account.
 *
 * The shape of the form follows the provider. One supplies its own base URL and
 * shows it read-only; one derives the URL from an account id and asks for that
 * instead; one reads a local login and asks for no key. The sheet this replaces
 * showed all five fields for every provider and disabled Save until the lot was
 * filled in, which asked people to type things the engine already knew.
 */
export function AddGatewaySheet({
  providers,
  accounts,
  busy = false,
  flowResult,
  onSend,
  onClose,
}: {
  providers: readonly GatewayProviderRow[];
  /** The saved accounts, for a suggested name that is not already taken. */
  accounts: readonly AccountRow[];
  busy?: boolean;
  flowResult?: GatewayFlowResult | null;
  onSend: (action: { operation: string; extra: Record<string, unknown> }) => void;
  onClose: () => void;
}) {
  const [providerId, setProviderId] = useState(providers[0]?.id ?? '');
  const provider = providers.find((row) => row.id === providerId);

  const [name, setName] = useState('');
  const [nameTouched, setNameTouched] = useState(false);
  const [endpoint, setEndpoint] = useState(provider?.defaultEndpoint ?? '');
  const [apiKey, setApiKey] = useState('');
  const [revealKey, setRevealKey] = useState(false);
  const [accountId, setAccountId] = useState('');
  const [gatewayId, setGatewayId] = useState('');
  const [region, setRegion] = useState(syncRegion(provider, ''));
  const [connection, setConnection] = useState<ConnectionState>('untested');
  const [message, setMessage] = useState('');

  const needsKey = requiresApiKey(provider);
  const custom = usesCustomEndpoint(provider);
  const regions = provider?.regions ?? [];

  // Switching provider changes which fields mean anything, so every derived
  // value has to be re-derived: the endpoint, the region, and the test result.
  // Leaving a test result from the previous provider on screen would say
  // "Connected" about an endpoint that is no longer on the sheet.
  const [pendingProvider, setPendingProvider] = useState(providerId);
  if (pendingProvider !== providerId) {
    setPendingProvider(providerId);
    setEndpoint(provider?.defaultEndpoint ?? '');
    setRegion(syncRegion(provider, ''));
    setConnection('untested');
    setMessage('');
  }

  useEffect(() => {
    if (!nameTouched && name === '') {
      setName(suggestedGatewayName(accounts, provider?.label ?? '', custom));
    }
  }, [accounts, custom, name, nameTouched, provider?.label]);

  // Every answer is matched to the command it answers, so a `gateway-test` from
  // another sheet cannot land on this one's connection row.
  const seenResult = useMemo(() => ({ current: '' }), []);
  useEffect(() => {
    if (!flowResult || flowResult.operation !== 'gateway-test') return;
    if (flowResult.result.requestId === seenResult.current) return;
    seenResult.current = flowResult.result.requestId;
    if (connection !== 'testing') return;
    setConnection(flowResult.result.status === 'success' ? 'available' : 'unavailable');
    setMessage(flowResult.result.message);
  }, [connection, flowResult, seenResult]);

  // Any change to what is being tested makes the previous answer stale.
  const [tested, setTested] = useState('');
  const testKey = `${endpoint}|${apiKey}|${accountId}`;
  if (tested !== testKey) {
    setTested(testKey);
    setConnection((current) => (current === 'testing' ? current : 'untested'));
    setMessage('');
  }

  const canSave = gatewayCanSave({
    provider,
    name,
    apiKey,
    accountId,
    endpoint,
    region,
    busy,
  });

  const save = () => {
    if (!provider) return;
    // A provider-declared API key is an *account*, not a gateway: the engine
    // stores it in the account store and the region is the credential profile
    // it is filed under, so the two commands are not interchangeable.
    const keyAccount = isApiKeyAccount(provider);
    onSend({
      operation: keyAccount ? 'account-save' : 'gateway-create',
      extra: keyAccount
        ? {
            providerId: provider.id,
            name: name.trim(),
            label: name.trim(),
            apiKey: apiKey.trim(),
            region: regions.length > 0 ? region : undefined,
          }
        : {
            providerId: provider.id,
            name: name.trim(),
            label: name.trim(),
            endpoint: custom ? endpoint.trim() : undefined,
            apiKey: needsKey ? apiKey.trim() : undefined,
            // The engine's own default applies when the field is left blank; a
            // prefilled "default" the user cannot see is a value they have to
            // delete before typing their own.
            gatewayId: provider.requiresGatewayId ? gatewayId.trim() || 'default' : undefined,
            // An account-derived gateway is probed and stored by its account
            // id; the engine builds the URL. `accountId` is not a wire field.
            profile: provider.requiresAccountId ? accountId.trim() : undefined,
          },
    });
  };

  const test = () => {
    if (!provider) return;
    setConnection('testing');
    setMessage('');
    onSend({
      operation: 'gateway-test',
      extra: {
        providerId: provider.id,
        name: 'test',
        endpoint: resolvedEndpoint(provider, endpoint).trim(),
        apiKey: needsKey ? apiKey.trim() : undefined,
        // An account-scoped gateway is probed by its account id; the engine
        // derives the URL that goes with it.
        profile: provider.requiresAccountId ? accountId.trim() : undefined,
      },
    });
  };

  return (
    <Dialog
      title="Add API Gateway"
      onCancel={onClose}
      testId="gateway-flow.provider.dialog"
      width="form"
      actions={
        <>
          <Button kind="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            kind="primary"
            testId="gateway-flow.save"
            disabled={!canSave}
            onClick={save}
          >
            Add Gateway
          </Button>
        </>
      }
    >
      <Group
        title="Gateway"
        footer="Choose a provider to show only the fields it needs. This name is how the gateway appears in Accounts & APIs."
        testId="gateway-flow.gateway"
      >
        <Row>
          <RowText title="Provider" detail={provider?.detail} />
          <RowActions>
            {/* The brand mark leads each item, as `AddGatewaySheet.swift:96`
                passes the provider to `DesignProviderIcon`. */}
            <MenuSelect
              label=""
              ariaLabel="Gateway provider"
              title="Choose the service that issued this gateway’s credentials."
              value={providerId}
              testId="gateway-flow.provider.picker"
              onChange={setProviderId}
              options={providers.map((row) => ({
                value: row.id,
                label: row.label,
                provider: row.id,
              }))}
            />
          </RowActions>
        </Row>
        <Row>
          <Field
            label="Name"
            value={name}
            help="A short label to recognize this gateway in Accounts & APIs."
            onChange={(value) => {
              setName(value);
              setNameTouched(true);
            }}
            testId="gateway-flow.name"
          />
        </Row>
      </Group>

      <Group title="Credentials" footer={connectionFooter({ state: connection, provider, message, endpoint, busy })}>
        {needsKey ? (
          <Row>
            <Field
              label="API Key"
              value={apiKey}
              help="Enter the API key issued by this provider."
              onChange={setApiKey}
              type={revealKey ? 'text' : 'password'}
              testId="gateway-flow.api-key"
            />
            <RowActions>
              <Button
                kind="secondary"
                ariaLabel={revealKey ? 'Hide API Key' : 'Show API Key'}
                testId="gateway-flow.api-key.reveal"
                onClick={() => setRevealKey((current) => !current)}
              >
                {revealKey ? 'Hide' : 'Show'}
              </Button>
            </RowActions>
          </Row>
        ) : (
          <>
            <Row>
              <RowText
                title="No key needed"
                detail={providerId === 'grok' ? 'Uses your Grok login session (~/.grok/auth.json).' : undefined}
              />
            </Row>
            <Row>
              <RowText detail="Run: grok login" />
            </Row>
          </>
        )}

        {provider?.requiresAccountId ? (
          <Row>
            <Field
              label="Account ID"
              value={accountId}
              help="Enter the account ID from this provider’s dashboard."
              onChange={setAccountId}
              testId="gateway-flow.account-id"
            />
          </Row>
        ) : null}

        {provider?.requiresGatewayId ? (
          <Row>
            <Field
              label="Gateway ID"
              value={gatewayId}
              help="Enter the AI Gateway ID or name shown by this provider."
              onChange={setGatewayId}
              testId="gateway-flow.gateway-id"
            />
          </Row>
        ) : null}

        {regions.length > 0 ? (
          <Row testId="gateway-flow.region">
            <RowText title="Region" />
            <RowActions>
              <Select
                label=""
                ariaLabel="Region"
                value={region}
                testId="gateway-flow.region.picker"
                onChange={setRegion}
                options={regions.map((value) => ({ value, label: value }))}
              />
            </RowActions>
          </Row>
        ) : providerSuppliesEndpoint(provider) ? (
          // Read-only, not a field. The engine has this value; a text box would
          // invite a user to break it by typing a URL that differs by a slash.
          <Row testId="gateway-flow.endpoint">
            <RowText
              title="Endpoint"
              detail={provider?.defaultEndpoint || `Provided by ${provider?.label ?? providerId}`}
            />
          </Row>
        ) : (
          <Row>
            <Field
              label="Endpoint"
              value={endpoint}
              help="Enter the provider’s base URL, including https:// and any required path."
              onChange={setEndpoint}
              testId="gateway-flow.endpoint.input"
            />
          </Row>
        )}

        {/* A provider-declared key is filed into an account store, not routed
            as a gateway, so there is no endpoint of its own to probe. */}
        {!isApiKeyAccount(provider) ? (
          <Row testId="gateway-flow.connection">
            <RowText title="Connection" />
            <RowActions>
              {/*
                Each state carries its own id rather than one row whose id is
                interpolated from the state: the three states are three
                different answers, and an inventory that only sees
                `gateway-flow.{x}` cannot tell which ones the app can show.
              */}
              {connection === 'testing' ? (
                <span
                  className="text-[12px] text-secondary"
                  role="status"
                  data-testid="gateway-flow.testing"
                >
                  Testing…
                </span>
              ) : null}
              {connection === 'available' ? (
                <span className="inline-flex" data-testid="gateway-flow.available">
                  <StatusLabel status="ready" />
                </span>
              ) : null}
              {connection === 'unavailable' ? (
                <span className="inline-flex" data-testid="gateway-flow.unavailable">
                  <StatusLabel status="unavailable" />
                </span>
              ) : null}
              <Button
                kind="secondary"
                testId="gateway-flow.test"
                disabled={connection === 'testing' || !connectionTestEnabled({ provider, apiKey, accountId, busy })}
                onClick={test}
              >
                {TEST_LABEL[connection]}
              </Button>
            </RowActions>
          </Row>
        ) : null}
      </Group>
    </Dialog>
  );
}

export { TEST_STATE_LABEL };
