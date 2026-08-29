import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const ROOT = join(import.meta.dirname, '..');

interface NodeCoverage {
  id: string;
  design: string;
  files: string[];
  tokens: string[];
}

const macOS = (name: string): string => join('src', 'macos', name);
const native = (name: string): string => join('src', 'macos', name);

// These are the exact Pencil nodes in the handoff. Caption nodes intentionally
// point at the same production surface as their related screen frame.
const coverage: NodeCoverage[] = [
  {
    id: 'd8hjQE',
    design: '01. Tray',
    files: [macOS('MenuBarMenu.swift')],
    tokens: ['MenuBarMenu', 'MenuBarProxiesRow'],
  },
  {
    id: 'lXvu5',
    design: '03. Proxy Hub',
    files: [macOS('ProxyHubView.swift')],
    tokens: ['ProxyHubView', 'Published Models'],
  },
  {
    id: 'dlHpg',
    design: '30A · Dev Flow · Apps',
    files: [macOS('AppSheets.swift')],
    tokens: ['case connection', 'case applying', 'case recovery'],
  },
  {
    id: 'TacTH',
    design: '03. Accounts & APIs',
    files: [
      macOS('AccountsView.swift'),
      macOS('AccountFlows.swift'),
      macOS('AccountDetailView.swift'),
    ],
    tokens: [
      'Add Account…',
      'Add API Gateway…',
      'Make Current',
      'On This Mac',
      'New Account',
      'Account Name',
      'Account',
      'Remaining Quotas',
    ],
  },
  {
    id: 'RtGtn',
    design: '05H · Discovery · Add Account · On This Mac',
    files: [macOS('AccountFlows.swift')],
    tokens: [
      'AddSavedAccountSheet',
      'On This Mac',
      'New Account',
      'Account Name',
      'Check Again',
      'Sign Out on This Mac',
      'AnyPick saves this account so you can switch back anytime.',
      'account-flow.identity',
    ],
  },
  {
    id: 'qoZdv',
    design: '05K · Discovery · Add Account · Browser Sign-In',
    files: [macOS('AccountFlows.swift')],
    tokens: [
      'case newAccount = "New Account"',
      'Sign In with \\(providerLabel)',
      'Opens \\(providerLabel)’s official browser flow',
      'sourceMode == .thisMac',
    ],
  },
  {
    id: 'ufmhY',
    design: 'Manage · Account Inventory',
    files: [macOS('DesignTypes.swift'), macOS('AccountsView.swift'), macOS('AccountFlows.swift')],
    tokens: [
      'Accounts & APIs',
      'All Providers',
      'API Gateways',
      'Button("Switch")',
      'Button("Connect")',
      'ConnectGatewaySheet',
      'EditAccountSheet',
    ],
  },
  {
    id: 'd7cMTt',
    design: 'Proxies & Activity',
    files: [macOS('ProxiesView.swift'), macOS('ActivityView.swift')],
    tokens: ['Proxies', 'Activity'],
  },
  {
    id: 'Hjc87',
    design: 'Operate and diagnose',
    files: [macOS('ActivityView.swift')],
    tokens: ['Events', 'Logs'],
  },
  {
    id: 'ScRCx',
    design: 'A · PROXIES',
    files: [macOS('ProxiesView.swift')],
    tokens: ['DesignIdentityRow'],
  },
  {
    id: 'xNdi0',
    design: '1 · Service Index',
    files: [macOS('ProxiesView.swift')],
    tokens: ['ForEach(model.proxies)'],
  },
  {
    id: 'WGz03',
    design: '2 · Service Detail',
    files: [macOS('ProxyDetailView.swift')],
    tokens: ['Service', 'Endpoint'],
  },
  {
    id: 'RCx0b',
    design: 'Proxies · Index',
    files: [macOS('ProxiesView.swift')],
    tokens: ['DesignRouteID.proxy'],
  },
  {
    id: 'EpMI0',
    design: 'Account Proxy · Detail',
    files: [macOS('ProxyDetailView.swift')],
    tokens: ['Restart Proxy', 'Stop Proxy'],
  },
  {
    id: 'sRxJ8',
    design: 'Transition · Open Proxy',
    files: [macOS('ProxiesView.swift'), macOS('MainWindow.swift')],
    tokens: ['NavigationLink', 'ProxyDetailView'],
  },
  {
    id: 'U5W6zu',
    design: 'C · SERVICE STATE MATRIX',
    files: [macOS('DesignTypes.swift')],
    tokens: ['DesignServiceOperation'],
  },
  {
    id: 'K6gjVr',
    design: 'Ready/stopped/waiting/loading/failure',
    files: [macOS('ProxyDetailView.swift')],
    tokens: ['ProgressView', 'Needs Attention'],
  },
  {
    id: 'wm697',
    design: 'Service State Matrix',
    files: [
      macOS('ProxyHubView.swift'),
      macOS('ProxyDetailView.swift'),
      macOS('ActivityView.swift'),
    ],
    tokens: ['Couldn’t Start', 'serviceFailures', 'Couldn’t Load Logs'],
  },
  {
    id: 'W5ipg',
    design: 'Account Backups',
    files: [macOS('BackupsView.swift')],
    tokens: ['Backup files contain working credentials'],
  },
  {
    id: 'ROXiu',
    design: 'Export/import/recovery',
    files: [macOS('BackupFlows.swift')],
    tokens: ['Export Accounts', 'Import Accounts'],
  },
  {
    id: 'EZq2K',
    design: '1 · Backup Inventory',
    files: [macOS('BackupsView.swift')],
    tokens: ['ForEach(model.backups)'],
  },
  {
    id: 'b8C7u0',
    design: 'Protection status and snapshots',
    files: [macOS('BackupsView.swift')],
    tokens: ['protected staging copy', 'original backup file is not deleted'],
  },
  {
    id: 'Bx86p',
    design: 'Backup Inventory',
    files: [macOS('BackupsView.swift')],
    tokens: ['Reviewed Backups', 'Forget Review…'],
  },
  {
    id: 'kjAmI',
    design: 'Onboarding & Settings',
    files: [macOS('OverviewView.swift'), macOS('SettingsView.swift')],
    tokens: ['Welcome to AnyPick', 'SettingsView'],
  },
  {
    id: 'I3etVz',
    design: 'First run and preferences',
    files: [macOS('OverviewView.swift'), macOS('MainWindow.swift')],
    tokens: ['FirstRunWelcomeSheet', 'showFirstRunWelcome'],
  },
  {
    id: 'Vr8xa',
    design: 'A · FIRST RUN',
    files: [macOS('OverviewView.swift')],
    tokens: ['Get Started'],
  },
  {
    id: 'UuMq4',
    design: '1 · Welcome',
    files: [macOS('OverviewView.swift')],
    tokens: ['Welcome to AnyPick'],
  },
  {
    id: 'g1Oa4',
    design: '2 · First Connection Journey',
    files: [macOS('OverviewView.swift')],
    tokens: ['Save a Login', 'Point an App'],
  },
  {
    id: 'WEopJ',
    design: 'First Run · Welcome',
    files: [macOS('OverviewView.swift')],
    tokens: ['Local by default', 'Continue'],
  },
  {
    id: 'KA3Ao',
    design: 'First Run · Account Journey',
    files: [macOS('OverviewView.swift')],
    tokens: ['Use an App', 'Set Up Proxy Hub'],
  },
  {
    id: 'GozwU',
    design: 'App',
    files: [macOS('AppEntry.swift'), macOS('MainWindow.swift')],
    tokens: ['Window("AnyPick"', 'NavigationSplitView'],
  },
  {
    id: 'rOF3g',
    design: 'B · ACTIVITY',
    files: [macOS('ActivityView.swift')],
    tokens: ['Activity'],
  },
  {
    id: 'T7vALZ',
    design: '1 · Events',
    files: [macOS('ActivityView.swift')],
    tokens: ['labels: ["Events", "Live Logs"]'],
  },
  {
    id: 'pDROW',
    design: '2 · Live Logs',
    files: [macOS('ActivityView.swift')],
    tokens: ['labels: ["Events", "Live Logs"]', 'Search Logs'],
  },
  {
    id: 'I3xVl',
    design: 'Back Up, Restore and Clean Up',
    files: [macOS('BackupFlows.swift'), macOS('BackupsView.swift')],
    tokens: ['Replace Existing Accounts?', 'Forget This Backup Review?'],
  },
  {
    id: 's3WNb',
    design: 'Native confirmation and recovery',
    files: [macOS('BackupFlows.swift')],
    tokens: ['No saved accounts were changed', 'Turn off an account to leave it out'],
  },
  {
    id: 'l69js',
    design: 'B · SETTINGS SCENE',
    files: [macOS('AppEntry.swift')],
    tokens: ['Settings {'],
  },
  {
    id: 'ULbNA',
    design: 'General',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['SettingsGeneralPane'],
  },
  {
    id: 'v1PgC',
    design: 'Security',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['SettingsSecurityPane'],
  },
  {
    id: 'TPmWA',
    design: 'Usage',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['SettingsUsagePane'],
  },
  {
    id: 'hbc8R',
    design: 'Proxies',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['SettingsLocalProxiesPane'],
  },
  {
    id: 'WU0tF',
    design: 'Activity · Events',
    files: [macOS('ActivityView.swift')],
    tokens: ['filteredActivity', 'Search Events'],
  },
  {
    id: 'xKxkF',
    design: 'Activity · Live Logs',
    files: [macOS('ActivityView.swift')],
    tokens: ['filteredLogLines', 'model.selectLogSource(source.id)', 'activity.logs.live'],
  },
  {
    id: 'ytUWF',
    design: 'Operations Contract',
    files: [macOS('Models.swift'), native('ProductionModel.swift')],
    tokens: ['undoAction', 'switchPhase = .recovery'],
  },
  {
    id: 'm27TTQ',
    design: 'Backup Task Flow',
    files: [macOS('BackupFlows.swift'), native('ProductionModel.swift')],
    tokens: ['case .importing', 'case .atomicFailure'],
  },
  {
    id: 'D2Yy1M',
    design: 'Behavior and startup',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['Open at Login'],
  },
  {
    id: 'y27oM',
    design: 'Settings · General',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['Getting Started', 'About'],
  },
  {
    id: 'dvqvK',
    design: 'Credential protection',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['Hide Tokens', 'Local Access Only'],
  },
  {
    id: 'y6ZqXG',
    design: 'Settings · Security',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['API keys and proxy tokens never appear'],
  },
  {
    id: 'XA2NA',
    design: 'Quota display and polling',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['Show Usage Remaining', 'Quota Guard'],
  },
  {
    id: 'aGxm7',
    design: 'Settings · Usage',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['Compatibility Proxy Pools'],
  },
  {
    id: 'bxqaQ',
    design: 'Service defaults',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['Start Enabled Proxies at Launch'],
  },
  {
    id: 'nfKFW',
    design: 'Settings · Proxies',
    files: [macOS('SettingsPanes.swift')],
    tokens: ['Open Proxies…'],
  },
];

describe('macOS Pencil node coverage', () => {
  it('keeps every requested node mapped to a real implementation surface', async () => {
    const ids = coverage.map(({ id }) => id);
    expect(coverage).toHaveLength(53);
    expect(new Set(ids).size).toBe(53);

    for (const entry of coverage) {
      const source = (
        await Promise.all(entry.files.map((file) => readFile(join(ROOT, file), 'utf8')))
      ).join('\n');
      for (const token of entry.tokens) {
        expect(source, `${entry.id} (${entry.design}) is missing ${token}`).toContain(token);
      }
    }
  });
});
