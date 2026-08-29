import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { readNativeTraySwiftBundle } from '../src/tray/native-sources';

const ROOT = join(import.meta.dirname, '..');

async function macOSDesignBundle(): Promise<string> {
  const directory = join(ROOT, 'src', 'macos');
  const manifest = await readFile(join(directory, 'sources.txt'), 'utf8');
  const names = manifest
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.endsWith('.swift'));
  return (await Promise.all(names.map((name) => readFile(join(directory, name), 'utf8')))).join(
    '\n\n',
  );
}

describe('macOS design-to-production feature parity', () => {
  it('has a complete production command path for every interactive design flow', async () => {
    const design = await macOSDesignBundle();
    const native = await readNativeTraySwiftBundle(join(ROOT, 'src', 'macos'));
    const mutations = await readFile(join(ROOT, 'src', 'tray', 'supervisor-mutations.ts'), 'utf8');
    const production = `${native}\n${mutations}`;

    const matrix = [
      {
        flow: 'overview attention and recovery',
        design: ['OverviewView', 'Review'],
        production: ['ProductionDesignModel', 'routingConflicts = store.hubConflicts.map'],
      },
      {
        flow: 'app index, detail, and model editing',
        design: ['AppsView', 'AppDetailView', 'AppModelsSheet'],
        production: [
          'ProductionDesignModel',
          'store.managedAppClientIDs.compactMap(designApp)',
          'case "grok": return "Grok"',
          'store.applyModelRoles',
          'case "direct-account": return .native',
        ],
      },
      {
        flow: 'capture account',
        design: ['AddSavedAccountSheet', 'This Mac', 'New Account', 'func addSavedAccount('],
        production: ['operation: "account-detect"', 'operation: "account-save"'],
      },
      {
        flow: 'edit, make current, refresh, and remove account',
        design: [
          'func updateAccount(',
          'func makeAccountCurrent(',
          'func refreshAccount(',
          'func removeAccount(',
        ],
        production: [
          'account.switchActionId',
          'operation: "account-edit"',
          'operation: "account-refresh"',
          'operation: "account-remove"',
        ],
      },
      {
        flow: 'create and test gateway',
        design: ['AddGatewaySheet', 'func addGateway('],
        production: ['operation: "gateway-test"', 'gateway-create', 'gateway-edit'],
      },
      {
        flow: 'connect gateway to apps',
        design: ['ConnectGatewaySheet', 'func connectGateway('],
        production: ['override func connectGateway(', 'func setRoute(appID:'],
      },
      {
        flow: 'route and model roles',
        design: [
          'func updateAppConfiguration(',
          'Apply Changes',
          'case applying',
          'case recovery',
          '$0.kind == .account',
          '$0.kind == .hub',
          '$0.kind == .gateway',
        ],
        production: [
          'func invoke(_ action: ActionSnapshot)',
          'func applyModelRoles(',
          'kind: designRouteKind($0.routeKind)',
          'providerID: $0.upstreamProviderId',
        ],
      },
      {
        flow: 'Proxy Hub sources',
        design: [
          'case .proxyHub: ProxyHubView(model: model)',
          'func setHubSource(',
          'PublishedModelsView',
          'Overlapping models',
        ],
        production: [
          'operation: "hub-source-toggle"',
          'store.hubSources.map',
          'hubPublishedModels',
        ],
      },
      {
        flow: 'local proxy lifecycle',
        design: [
          'case .proxies: ProxiesView(model: model)',
          'case .proxies: "Proxies"',
          'func setProxyEnabled(',
          'func restartRunningProxies(',
          'func stopAllProxies(',
          'serviceOperations',
          'serviceFailures',
        ],
        production: [
          'operation: "proxy-restart-all"',
          'operation: "proxy-stop-all"',
          'private func proxyIsRequired(',
          'serviceRequests[requestID]',
          'serviceOperations[serviceID]',
        ],
      },
      {
        flow: 'proxy detail and recovery',
        design: ['ProxiesView', 'ProxyDetailView'],
        production: [
          'ProductionDesignModel',
          'store.proxies.filter { $0.providerId != "proxy-hub" }.map(designProxy)',
        ],
      },
      {
        flow: 'production logs and activity',
        design: [
          'activityShowsLogs',
          'model.selectLogSource(source.id)',
          'Button("Clear Log…", role: .destructive)',
          'func clearActivity()',
        ],
        production: [
          'override func setActivityShowsLogs(',
          'override func selectLogSource(',
          'override func refreshSelectedLogSource()',
          'override func clearActivity()',
          'override func setLogPolling(',
          'store.logStatesBySource',
          'liveLogLines = designLogLines()',
          'func requestLogs(',
        ],
      },
      {
        flow: 'selective backup export and import',
        design: ['CreateBackupSheet', 'RestoreBackupSheet'],
        production: [
          'operation = "account-backup-export"',
          'operation = "account-backup-import"',
          'synchronizeBackupInspectionState()',
        ],
      },
      {
        flow: 'app reset and settings',
        design: [
          'func resetAppConfiguration(',
          'Open at Login',
          'Start Enabled Proxies at Launch',
          'Quota Guard',
        ],
        production: [
          'operation: "client-reset"',
          'operation: "setting-launch-at-login"',
          'override func setStartProxiesAtLaunch(',
          'operation: "setting-auto-start-proxies"',
          'override func setWarnBeforeLowUsage(',
          'operation: "setting-quota-guard"',
          'startProxiesAtLaunch = store.settings.startEnabledProxies',
          'warnBeforeLowUsage = store.settings.quotaGuardEnabled',
        ],
      },
      {
        flow: 'first-run onboarding and global content states',
        design: [
          'DesignLoadingState',
          'DesignEmptyState',
          'shouldShowOnboarding',
          'FirstRunWelcomeSheet',
          'func dismissOnboarding(',
          'Get Started',
        ],
        production: [
          'phase = store.snapshotReady ? .ready : .loading',
          'DesignEmptyState',
          'TrayPreferences.onboardingDismissed',
          'override func dismissOnboarding(',
        ],
      },
      {
        flow: 'system appearance (no product Light/Dark picker)',
        design: ['DesignAppearance', 'followingDesignAppearance(model.appearance)'],
        production: ['appearance: .system', 'Window("AnyPick", id: "main")'],
      },
      {
        flow: 'Power Quick Panel menu-bar access for the full app',
        design: [
          'MenuBarMenu',
          'MenuBarOtherAppsRow',
          'MenuBarSearchResults',
          'MenuBarProxiesRow',
          'model.setProxyHubEnabled($0)',
          'model.setProxyEnabled(id: proxy.id, enabled: $0)',
          'func setQuickPanelShown(',
          'var quickPanelApps',
        ],
        production: [
          '.menuBarExtraStyle(.window)',
          'ProductionDesignModel',
          'showInQuickPanel: AppPreferences.quickPanelShown(for: clientID)',
        ],
      },
    ] as const;

    for (const feature of matrix) {
      for (const token of feature.design) {
        expect(design, `${feature.flow}: missing design token ${token}`).toContain(token);
      }
      for (const token of feature.production) {
        expect(production, `${feature.flow}: missing production token ${token}`).toContain(token);
      }
    }
  });
});
