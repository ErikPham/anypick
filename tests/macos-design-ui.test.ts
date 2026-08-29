import { readdir, readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { beforeAll, describe, expect, it } from 'vitest';

const ROOT = join(import.meta.dirname, '..');
const MACOS_DIR = join(ROOT, 'src', 'macos');
const LAUNCHER = join(ROOT, 'scripts', 'demo-macos-design.mjs');

function sourceNames(manifest: string): string[] {
  return manifest
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith('#') && line.endsWith('.swift'));
}

function fixtureCompiledSource(_name: string, source: string): string {
  return source;
}

describe('macOS app UI', () => {
  let names: string[];
  let swiftFiles: string[];
  let sources: Map<string, string>;
  let bundle: string;

  beforeAll(async () => {
    names = sourceNames(await readFile(join(MACOS_DIR, 'sources.txt'), 'utf8'));
    swiftFiles = (await readdir(MACOS_DIR)).filter((name) => name.endsWith('.swift')).toSorted();
    const entries = await Promise.all(
      swiftFiles.map(
        async (name) => [name, await readFile(join(MACOS_DIR, name), 'utf8')] as const,
      ),
    );
    sources = new Map(entries);
    bundle = entries.map(([name, source]) => fixtureCompiledSource(name, source)).join('\n\n');
  });

  it('keeps a complete, unique Swift source manifest', () => {
    expect(names.length).toBeGreaterThan(0);
    expect(new Set(names).size).toBe(names.length);
    expect(names.every((name) => /^[A-Za-z0-9][A-Za-z0-9_-]*\.swift$/.test(name))).toBe(true);
    expect(names.toSorted()).toEqual(swiftFiles);
    expect(sources.size).toBe(names.length);
  });

  it('keeps pane sources off the credential and supervisor boundary', () => {
    const runtime = new Set([
      'Store.swift',
      'StoreTransfers.swift',
      'ProductionModel.swift',
      'AppDelegate.swift',
      'SnapshotTypes.swift',
    ]);
    const panes = [...sources.entries()]
      .filter(([name]) => !runtime.has(name))
      .map(([, source]) => source)
      .join('\n');
    expect(panes).not.toContain('ANYPICK_HOME');
    expect(panes).not.toContain('~/.anypick');
    expect(panes).not.toContain('FileHandle.standardInput');
  });

  it('uses native scenes for the main window, menu bar, and Settings', () => {
    const appEntry = sources.get('AppEntry.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';
    const settings = sources.get('SettingsView.swift') ?? '';
    const panes = sources.get('SettingsPanes.swift') ?? '';

    expect(appEntry).toMatch(/\bWindow\s*\(\s*['"]AnyPick['"]/);
    expect(appEntry).not.toContain('WindowGroup');
    expect(appEntry).toContain('MenuBarExtra');
    expect(appEntry).toContain('MenuBarMenu(model: model)');
    expect(appEntry).toMatch(/\.menuBarExtraStyle\s*\(\s*\.window\s*\)/);
    expect(appEntry).toMatch(/\bSettings\s*\{/);
    expect(appEntry).toContain('SidebarCommands()');
    expect(appEntry).not.toContain('CommandGroup(replacing: .appSettings)');
    expect(sources.get('MainWindow.swift') ?? '').toContain('.toolbar(removing: .sidebarToggle)');
    expect(appEntry).toContain('struct AppCommands');
    expect(appEntry).toContain('CommandMenu("Go")');
    expect(models).toContain('environment["ANYPICK_MACOS_APPEARANCE"] ?? "system"');
    expect(appEntry.match(/\.followingDesignAppearance\(/g)?.length ?? 0).toBeGreaterThanOrEqual(2);
    // System appearance only — no Light/Dark product picker.
    expect(settings + panes).not.toContain('Picker("Appearance"');
    expect(panes).toContain('Start Enabled Proxies at Launch');
    expect(panes).toContain('Pick any. Code on.');
  });

  it('uses a System Settings–style NavigationSplitView shell', () => {
    const mainWindow = sources.get('MainWindow.swift') ?? '';
    const types = sources.get('DesignTypes.swift') ?? '';

    expect(mainWindow).toContain('NavigationSplitView');
    expect(mainWindow).toContain('List(selection: $model.destination)');
    expect(mainWindow).toContain('.listStyle(.sidebar)');
    expect(mainWindow).toContain('.searchable(text: $sidebarSearch, placement: .sidebar');
    expect(mainWindow).toContain('ForEach(filteredDestinations)');
    expect(mainWindow).toContain('minWidth: 820');
    expect(mainWindow).toContain('minHeight: 560');
    expect(types).toContain('case proxyHub');
    expect(types).toContain('case .proxyHub: "Proxy Hub"');
    expect(types).toContain('case .proxies: "Proxies"');
    expect(mainWindow).toContain('case .proxyHub: ProxyHubView(model: model)');
    expect(mainWindow).toContain('case .proxies: ProxiesView(model: model)');
    // Grouped Form destinations (System Settings pane pattern)
    expect(bundle).toMatch(/\.formStyle\s*\(\s*\.grouped\s*\)/);
    expect(bundle).toMatch(/\bForm\s*\{/);
    expect(bundle).toMatch(/\bPicker\s*\(/);
  });

  it('ships the Power Quick Panel in both production and the preview fixture', () => {
    const menu = [
      'MenuBarChrome.swift',
      'MenuBarSearch.swift',
      'MenuBarMenu.swift',
      'MenuBarHome.swift',
      'MenuBarFirstRun.swift',
      'MenuBarUseWith.swift',
      'MenuBarSwitching.swift',
    ]
      .map((name) => sources.get(name) ?? '')
      .join('\n');

    expect(menu).toContain('trayPrimaryAppLimit');
    expect(menu).toContain('apps.count > trayPrimaryAppLimit');
    expect(menu).toContain('MenuBarAttentionNotice');
    expect(menu).toContain('Proxy Hub');
    expect(menu).toContain('Proxies');
    expect(sources.get('MenuBarHome.swift') ?? '').toContain('struct MenuBarProxiesRow');
    expect(sources.get('MenuBarHome.swift') ?? '').toContain('Image(systemName: "server.rack")');
    expect(sources.get('MenuBarHome.swift') ?? '').toContain('Text("Proxies")');
    expect(menu).not.toContain('cable.connector');
    expect(menu).toContain('Other Apps');
    const menuHome = sources.get('MenuBarMenu.swift') ?? '';
    expect(menuHome).toContain('model.quickPanelApps');
    expect(menuHome).toContain('TrayRoutingHeader');
    expect(menuHome).toContain('MenuBarProxiesRow');
    expect(menu).toContain('model.setProxyHubEnabled($0)');
    expect(menu).toContain('model.setProxyEnabled(id: proxy.id, enabled: $0)');
    expect(menu).toContain('model.restartProxy(id: proxy.id)');
    expect(menu).toContain('.accessibilityValue(expanded ? "Expanded" : "Collapsed")');
    expect(menu).toContain('return "\\(attentionCount) issue · \\(totalRunningCount) running"');
    expect(menu).toContain('Search \\(count) saved');
    expect(menu).toContain('MenuBarSearchResults');
    expect(menu).toContain('No app, account, or route matches');
    expect(menu).toContain('tray.search.result.');
    expect(menu).toContain('activateSearchResult');
    expect(menu).toContain('Open AnyPick');
    expect(menu).toContain('Button(action: quit)');
    expect(menu).toContain('MenuBarAttentionNotice');
    expect(menu).toContain('dismissAttentionNotice');
    expect(menu).toContain('dismissedAttentionKeys');
    expect(menu).toContain('attentionMessage');
    expect(menu).toContain('actionTitle: "Hide"');
    expect(menu).not.toContain('login unavailable');
    expect(menu).not.toContain('Claude & Codex');
    expect(menu).not.toContain('Fix Now');
    expect(menu).toContain('Button("Switch"');
    expect(menu).not.toContain('Button("Switch…"');
    expect(menu).toContain('Needs attention');
    expect(menu).toContain('Save a login to start');
    expect(menu).toContain('Switching…');
    expect(menu).toContain('Try again');
    expect(menu).toContain('TrayInlineRouteMark');
    expect(menu).not.toContain('MenuBarSwitchBanner');
    // Keep the restored tray implementation covered: searchable saved accounts
    // stay in the original capped scroll region above shared routing.
    const useWith = sources.get('MenuBarUseWith.swift') ?? '';
    expect(useWith).toContain('ScrollView {');
    expect(useWith).toContain('LazyVStack(spacing: 0)');
    expect(useWith).toContain('ForEach(Array(filteredAccountRoutes.enumerated())');
    expect(useWith).toContain('Accounts ·');
    expect(useWith).toContain('Or Use Shared');
    expect(useWith).toContain('API Gateways');
    expect(useWith).toContain('keyboardShortcut("f"');
    expect(useWith).not.toContain('TrayPaint.accent.opacity');
    expect(useWith).toContain('TrayPaint.recessedFill');
    expect(useWith).toContain('RoundedRectangle');
    expect(sources.get('MenuBarChrome.swift') ?? '').toContain('Color(nsColor: .systemGreen)');
    expect(sources.get('MenuBarChrome.swift') ?? '').toContain('Color(nsColor: .systemOrange)');
    expect(sources.get('MenuBarPreview.swift') ?? '').toContain(
      'Color(nsColor: .underPageBackgroundColor)',
    );
    expect(sources.get('MenuBarPreview.swift') ?? '').toContain(
      '.background(.regularMaterial, in: RoundedRectangle',
    );
    expect(sources.get('MenuBarFirstRun.swift') ?? '').toContain('Save This Login');
    expect(sources.get('MenuBarFirstRun.swift') ?? '').toContain('model.saveCurrentLogin(login)');
    expect(sources.get('AccountsView.swift') ?? '').toContain('Search Accounts');
    expect(sources.get('AccountsView.swift') ?? '').toContain('Button("Add Account…")');
    expect(sources.get('AccountsView.swift') ?? '').not.toContain('Save Current Login');
    expect(sources.get('MenuBarUseWith.swift') ?? '').toContain('Switch \\(app.name) Account');
    const appEntry = sources.get('AppEntry.swift') ?? '';
    expect(appEntry).toContain('MenuBarMenu(model: model)');
    expect(appEntry).toContain('.menuBarExtraStyle(.window)');
    expect(appEntry).not.toContain('.menuBarExtraStyle(.menu)');
    expect(menu).toContain('setRoute(appID:');
    expect(menu).toContain('setProxyHubEnabled');
    expect(menu).toContain('NSAlert()');
    expect(menu).toContain('MenuBarUseWithSheet');
    expect(menu).not.toContain('NativeQuickPanel');
    expect(menu).not.toContain('QuickPanelMetrics');
    expect(menu).not.toContain('TraySpacing');
    expect(menu).toContain('RoundedRectangle');
    expect(menu).toContain('TextField(searchPrompt, text: $jump)');
    expect(menu).toContain('withAnimation(.easeInOut(duration: 0.16)) { expanded.toggle() }');
    expect(menu).toContain('if expanded {');
    expect(menu).not.toMatch(/\.font\(\.system\(size:\s*9\b/);
    expect(menu).toContain('.buttonStyle(.borderedProminent)');
    expect(menu).toContain('.buttonStyle(.bordered)');
  });

  it('uses native Form/List destinations instead of custom card chrome', () => {
    const apps = sources.get('AppsView.swift') ?? '';
    const connections = sources.get('AccountsView.swift') ?? '';
    expect(connections).toContain('Picker("Provider"');
    expect(connections).toContain('.pickerStyle(.menu)');
    expect(connections).toContain('Text("All Providers")');
    const hub = sources.get('ProxyHubView.swift') ?? '';
    const proxies = sources.get('ProxiesView.swift') ?? '';

    for (const source of [apps, connections, hub, proxies]) {
      expect(source).toMatch(/\b(?:Form\s*\{|DesignGroupedPane\s*\{)/);
      expect(
        /\.formStyle\s*\(\s*\.grouped\s*\)/.test(source) || source.includes('DesignGroupedPane'),
      ).toBe(true);
      expect(source).not.toMatch(/\bTable\s*\(/);
      expect(source).not.toMatch(/\.inspector\s*\(/);
      expect(source).not.toContain('DesignSurface');
    }
  });

  it('does not present model controls that are discarded on Save', () => {
    const models = sources.get('Models.swift') ?? '';
    const types = sources.get('DesignTypes.swift') ?? '';
    const sheets = sources.get('AppSheets.swift') ?? '';

    expect(types).toContain('var fastModel');
    expect(types).toContain('var reasoningModel');
    expect(types).toContain('var useCompatibleFallback');
    expect(sheets).toContain('model.updateAppModelRoles(');
    expect(sheets).toContain('modelRoles: values');
    expect(models).toContain('apps[index].fastModel = fastModel');
    expect(models).toContain('apps[index].reasoningModel = reasoningModel');
    expect(models).toContain('apps[index].useCompatibleFallback = useCompatibleFallback');
  });

  it('persists Quick Panel visibility and hides unchecked apps in the tray', () => {
    const apps = sources.get('AppsView.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';
    const prefs = sources.get('AppPreferences.swift') ?? '';
    const production = sources.get('ProductionModel.swift') ?? '';
    const menu = sources.get('MenuBarMenu.swift') ?? '';

    expect(apps).toContain('model.setQuickPanelShown(appID: app.id, shows: $0)');
    expect(models).toContain('AppPreferences.setQuickPanelShown(shows, for: appID)');
    expect(models).toContain('undoAction = .quickPanel(appID: appID, shows: previous)');
    expect(models).toContain('var quickPanelApps: [DesignApp]');
    expect(prefs).toContain('static let quickPanelHidden');
    expect(prefs).toContain('static let quickPanelShown');
    expect(prefs).toContain('static let dismissedAttention');
    expect(prefs).toContain('static var dismissedAttentionKeys');
    expect(prefs).toContain('static func quickPanelShown(for appID: String)');
    expect(production).toContain(
      'showInQuickPanel: AppPreferences.quickPanelShown(for: clientID) ?? true',
    );
    expect(menu).toContain('trayPrimaryApps(from: model.quickPanelApps)');
    expect(menu).toContain('trayOtherApps(from: model.quickPanelApps)');
    expect(menu).toContain('model.quickPanelApps.compactMap');
  });

  it('keeps SF Symbols, system fonts, and semantic colors', () => {
    const shared = sources.get('SharedUI.swift') ?? '';
    const mainWindow = sources.get('MainWindow.swift') ?? '';
    const types = sources.get('DesignTypes.swift') ?? '';

    expect(shared).toContain('struct DesignEmptyState');
    expect(shared).toContain('struct DesignWarningSymbol');
    expect(shared).toContain('.symbolRenderingMode(.multicolor)');
    expect(shared).toContain('systemImage: symbol');
    expect(shared).toContain('struct DesignGroupedPane');
    expect(shared).not.toContain('.foregroundStyle(.yellow)');
    expect(sources.get('MenuBarMenu.swift') ?? '').not.toContain('.foregroundStyle(.yellow)');
    expect(shared).not.toContain('struct DesignWrapLayout');
    expect(shared).not.toContain('struct DesignPaneBar');
    expect(shared).not.toMatch(/0x[0-9A-F]{6}/);
    expect(shared).not.toContain('fontFamily');
    expect(mainWindow).toContain('List(selection: $model.destination)');
    expect(mainWindow).toContain('.listStyle(.sidebar)');
    expect(mainWindow).toContain('.searchable(text: $sidebarSearch, placement: .sidebar');
    expect(bundle).not.toContain('Color.white.ignoresSafeArea');
    expect(types).toContain('case .overview: "square.grid.2x2"');
    expect(types).toContain('case .proxyHub: "point.3.connected.trianglepath.dotted"');
    expect(types).toContain('case .proxies: "server.rack"');
    expect(types).toContain('case .activity: "list.bullet"');
    // Ship SF Pro + SF Symbols only — no design-tool font/icon libraries in code.
    expect(bundle).not.toMatch(/\bInter\b|\bphosphor\b/i);
  });

  it('uses provider logos for identity rows and SF Symbols for chrome', () => {
    const brands = sources.get('BrandIcons.swift') ?? '';
    expect(brands).toContain('struct DesignProviderIcon');
    expect(brands).toContain('claude-mark.png');
    expect(brands).toContain('codex-app-light.png');
    expect(brands).toContain('gemini-mark-v2.png');
    expect(brands).toContain('kiro-app.png');
    expect(brands).toContain('openrouter-glyph-light.png');
    expect(brands).toContain('grok-mark-light.png');
    expect(brands).toContain('opencode-mark-light.png');
    expect(brands).toContain('Image(systemName: fallbackSymbol)');
    expect(brands).not.toMatch(/cornerRadius:\s*size \* 0\.22/);

    for (const name of [
      'AppsView.swift',
      'AccountsView.swift',
      'ProxiesView.swift',
      'ProxyHubView.swift',
      'AccountDetailView.swift',
      'AppDetailView.swift',
      'BackupFlows.swift',
    ]) {
      expect(sources.get(name) ?? '', name).toContain('DesignProviderIcon');
    }

    const chrome = [
      sources.get('MainWindow.swift') ?? '',
      sources.get('SettingsView.swift') ?? '',
      sources.get('BackupsView.swift') ?? '',
      sources.get('SharedUI.swift') ?? '',
    ].join('\n');
    expect(chrome).toContain('Image(systemName: destination.symbol)');
    expect(chrome).toContain('Image(systemName: item.symbol)');
    expect(chrome).toContain('square.and.arrow.up');
    expect(chrome).not.toContain('claude-mark.png');
  });

  it('keeps stable automation hooks and spoken labels', () => {
    const identifierCount = bundle.match(/\.accessibilityIdentifier\b/g)?.length ?? 0;
    expect(identifierCount).toBeGreaterThanOrEqual(20);
    expect(bundle).toMatch(/\.accessibilityLabel\b/);
    expect(bundle).toContain('.announcementRequested');
  });

  it('implements complete account and gateway management flows', () => {
    const accounts = sources.get('AccountsView.swift') ?? '';
    const flows = sources.get('AccountFlows.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';

    expect(accounts).toContain('.sheet(item: $activeSheet)');
    expect(accounts).toContain('AddSavedAccountSheet(model: model)');
    expect(accounts).toContain('AddGatewaySheet(model: model)');
    expect(accounts).not.toContain('private var revealsAction');
    expect(accounts).not.toContain('.onHover');
    expect(accounts).toContain('else if showsSwitch');
    expect(bundle).toContain('model.updateAccount(id: accountID');
    expect(bundle).toContain('model.refreshAccount(id: account.id)');
    expect(bundle).toContain('model.removeAccount(id: accountID)');
    expect(bundle).toContain('model.makeAccountCurrent(id: account.id)');
    expect(flows).toContain('"Check Again"');
    expect(flows).toContain('Sign Out on This Mac');
    expect(flows).toContain('On This Mac');
    expect(flows).toContain('New Account');
    expect(flows).toContain('DesignProviderPicker(options: providers, selection: $provider)');
    expect(flows).toContain('$0.id.caseInsensitiveCompare(option.id)');
    expect(flows).toContain('syncCurrentLogin(detectionMessage: result?.message)');
    expect(flows).toContain('.task(id: autoDetectionKey)');
    expect(flows).toContain('guard sourceMode == .thisMac, !provider.isEmpty');
    expect(flows).toContain('SecureField("", text: $apiKey)');
    expect(flows).toContain('private let fieldWidth: CGFloat = 220');
    expect(flows).toContain('.frame(width: fieldWidth)');
    expect(flows).toContain('Button("Test Connection"');
    expect(flows).toContain('ProgressView()');
    expect(flows).toContain('Label("Connected", systemImage: "checkmark.circle.fill")');
    expect(flows).toContain('Label("Not Connected", systemImage: "exclamationmark.circle.fill")');
    expect(flows).toContain('connectionMessage = result?.message ?? ""');
    expect(flows).toContain('onCancel: cancelAndDismiss');
    expect(flows).toContain('model.cancelAccountLogin()');
    expect(flows).toContain('groupsProviders: true');
    expect(flows).toContain('model.preferredGatewayProvider(for: targetClientID)');
    expect(flows).toContain('endpoint = suppliedEndpoint ?? ""');
    expect(flows).toContain('!isAPIKeyAccount && !providerSuppliesEndpoint');
    expect(flows).toContain('Account Name');
    expect(flows).toContain('account-flow.identity');
    expect(flows).toContain('.labelsHidden()');
    expect(flows).toContain('AccountInspectSheet');
    expect(flows).not.toContain('struct RemoveAccountSheet');
    expect(accounts).toContain('.confirmationDialog(');
    expect(accounts).toContain('Button("Remove Account", role: .destructive)');
    expect(models).toContain('func addSavedAccount(');
    expect(models).toContain('func addGateway(');
    expect(models).toContain('func removeAccount(id: String)');
  });

  it('reviews account changes, resets incompatible models, and offers undo', () => {
    const sheets = sources.get('AppSheets.swift') ?? '';
    const detail = sources.get('AppDetailView.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';
    const shared = sources.get('SharedUI.swift') ?? '';

    expect(sheets).toContain('case connection');
    expect(sheets).toContain('case applying');
    expect(sheets).toContain('case recovery');
    expect(sheets).toContain('Apply Changes');
    expect(sheets).toContain('Previous settings restored');
    expect(sheets).toContain('$0.kind == .account || $0.kind == .native');
    expect(detail).toContain(
      'guard let selectedRoute, selectedRoute.kind != .native else { return false }',
    );
    expect(detail).toContain('Change Account or Provider…');
    expect(detail).toContain('Button("Add Account…")');
    expect(detail).toContain('Button("Add API Gateway…")');
    expect(detail).toContain('AddGatewaySheet(model: model, targetClientID: appID)');
    expect(detail).toContain('model.connectGateway(id: gatewayID, appIDs: [appID])');
    expect(detail).toContain('ForEach(app.usageWindows)');
    expect(detail).not.toContain('LabeledContent("5-hour window"');
    expect(models).toContain('func updateAppConfiguration(');
    expect(sources.get('DesignTypes.swift') ?? '').toContain('case appConfiguration(DesignApp)');
    expect(models).toContain('func performUndo()');
    expect(shared).toContain('struct DesignSheetHost');
    expect(shared).toContain('struct DesignGroupedPane');
    expect(shared).toContain('.formStyle(.grouped)');
    expect(shared).toContain('.fixedSize(horizontal: false, vertical: true)');
    expect(shared).toContain('struct DesignSheetButton');
    expect(shared).toContain('button.bezelStyle = .rounded');
    expect(shared).toContain('.buttonStyle(.bordered)');
    expect(shared).not.toContain('.safeAreaInset(edge: .bottom)');
    expect(shared).not.toMatch(/if showsActions \{[\s\S]*?\bSection\s*\{/);
    expect(shared).not.toContain('ToolbarItemGroup(placement: .primaryAction)');
    expect(shared).not.toContain('ToolbarItem(placement: .cancellationAction)');
    expect(shared).toContain('DesignSheetButton(title: "Cancel"');
    expect(shared).not.toContain('.accessibilityLabel(title)');
    expect(shared).not.toContain('.background(.bar)');
    expect(sources.get('AppEntry.swift') ?? '').toContain('Button("Undo")');
    expect(models).toContain('func performUndo()');
  });

  it('uses stateful Proxy Hub sources and local proxy controls', () => {
    const hub = sources.get('ProxyHubView.swift') ?? '';
    const flows = sources.get('ProxyFlows.swift') ?? '';
    const proxies = sources.get('ProxiesView.swift') ?? '';
    const detail = sources.get('ProxyDetailView.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';

    expect(hub).toContain('model.setHubSource(source.id, enabled: $0)');
    expect(hub).not.toContain('.constant(');
    expect(hub).toContain('NavigationLink(value: DesignRouteID.publishedModels)');
    expect(hub).not.toContain('PublishedModelsSheet');
    expect(hub).not.toContain('Button("View")');
    expect(hub).toContain('model.selectRoutingCandidate(actionID: candidate.actionID)');
    expect(hub).toContain('Model Routing');
    expect(hub).toContain('One choice applies to every model ID');
    expect(hub).toContain('Picker(');
    expect(hub).not.toContain('Menu("Choose Account…")');
    expect(hub).not.toContain('joined(separator: ", ")');
    expect(hub).toContain('Button("Restart Hub")');
    expect(hub).not.toMatch(/Section\s*\{[^}]*Restart Hub/s);
    expect(flows).toContain('struct PublishedModelsView');
    expect(flows).toContain('ContentUnavailableView');
    expect(flows).toContain('model.publishedModels');
    expect(flows).toContain('Needs a Choice');
    expect(flows).not.toContain('DesignSheetHost');
    expect(models).toContain('func recompileHubRouting()');
    expect(models).toContain('DesignHubRouting.compile');
    expect(detail).toContain('confirmStop = true');
    expect(detail).toContain('model.setProxyEnabled(id: proxyID, enabled: false)');
    expect(detail).toContain('model.restartProxy(id: proxy.id)');
    expect(detail).toContain('model.serviceOperations[proxy.id]');
    expect(detail).toContain('model.serviceFailures[proxy.id]');
    expect(hub).toContain('model.serviceOperations["hub"]');
    expect(hub).toContain('Proxy Hub Couldn’t Start');
    expect(hub).toContain('Add a Compatible Account');
    expect(proxies).toContain('model.stopAllProxies()');
    expect(proxies).toContain('Stop All Proxies?');
    expect(models).toContain('func setProxyHubEnabled(_ enabled: Bool)');
    expect(models).toContain('func restartRunningProxies()');
  });

  it('implements selective backup creation, inspection, restore, and replacement', () => {
    const view = sources.get('BackupsView.swift') ?? '';
    const flows = sources.get('BackupFlows.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';

    expect(view).toContain('CreateBackupSheet(model: model)');
    expect(view).toContain('RestoreBackupSheet(');
    expect(view).toContain('model.importBackupPackage()');
    expect(view).toContain('Choose Backup…');
    expect(view).toContain('case .reading, .importing');
    expect(view).toContain('case .damaged');
    expect(view).toContain('case .newerVersion');
    expect(view).toContain('case .permissionDenied');
    expect(flows).toContain('case .atomicFailure');
    expect(flows).toContain('private let selectionLimit = 64');
    expect(flows).toContain('Replace Existing Accounts?');
    expect(flows).toContain('Button("Skip")');
    expect(flows).toContain('Button("Replace"');
    expect(models).toContain('func createBackup(accountIDs: Set<String>)');
    expect(models).toContain(
      'func restoreBackup(id: String, accountIDs: Set<String>, overwrite: Bool)',
    );
    expect(models).toContain('NSOpenPanel()');
  });

  it('provides native event/log switching, provider filtering, and log clearing', () => {
    const activity = sources.get('ActivityView.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';

    expect(activity).toContain('NSSegmentedControl(');
    expect(activity).toContain('labels: ["Events", "Live Logs"]');
    expect(activity).toContain('control.setAccessibilityRole(.tabGroup)');
    expect(activity).toContain('Section');
    expect(activity).not.toContain('LazyVStack');
    expect(activity).toContain('modeControl');
    expect(activity).toContain('Search Logs');
    expect(activity).toContain('Search Events');
    expect(activity).toContain('filteredLogLines');
    expect(activity).toContain('filteredActivity');
    expect(activity).toContain('logStateNotice');
    expect(activity).toContain('Couldn’t Load Logs');
    expect(activity).toContain('Proxy Is Not Running');
    expect(activity).toContain('Selected Log Is No Longer Available');
    expect(activity).toContain('model.setLogPolling(true)');
    expect(activity).toContain('model.setLogPolling(false)');
    expect(activity).toContain('Label("Live", systemImage: "dot.radiowaves.left.and.right")');
    expect(activity).toContain('Label("Filter Logs", systemImage: "line.3.horizontal.decrease")');
    expect(activity).toContain('model.selectLogSource(source.id)');
    expect(activity).toContain('Button("Clear Log…", role: .destructive)');
    expect(activity).not.toContain('Button("Refresh Log")');
    expect(activity).not.toContain('copyVisible');
    expect(models).toContain('func clearActivity()');
  });

  it('keeps Settings IA in a Settings scene with one home per preference', () => {
    const settings = sources.get('SettingsView.swift') ?? '';
    const panes = sources.get('SettingsPanes.swift') ?? '';
    const all = settings + panes;

    expect(settings).toContain('case general = "General"');
    expect(settings).toContain('case usage = "Usage"');
    expect(settings).toContain('case localProxies = "Proxies"');
    expect(settings).toContain('case security = "Security"');
    expect(panes).toContain('Toggle("Open at Login"');
    expect(panes).toContain('Toggle("Show Usage Remaining"');
    expect(panes).toContain('Start Enabled Proxies at Launch');
    expect((panes.match(/Show Usage Remaining/g) ?? []).length).toBe(1);
    expect((panes.match(/Start Enabled Proxies at Launch/g) ?? []).length).toBe(1);
    expect(all).not.toMatch(/tabItem\s*\{[^}]*Proxy Hub/s);
    expect(all).toContain('Open at Login');
    expect(all).toContain('Getting Started');
    expect(all).toContain('Show Usage Remaining');
    expect(all).toContain('Quota Guard');
    expect(all).not.toContain('Warn Below');
    expect(all).toContain('Hide Tokens');
    expect(all).toContain('Local Access Only');
    expect(all).toContain('Account Backups');
    expect(all).toContain('Unencrypted');
  });

  it('keeps Overview as a grouped Form pane, not a dashboard footer', () => {
    const overview = sources.get('OverviewView.swift') ?? '';

    expect(overview).toContain('Recent Activity');
    expect(overview).toContain('DesignGroupedPane');
    expect(overview).toContain('DesignWarningSymbol(scale: .medium)');
    expect(overview).not.toContain('buttonStyle(.plain)');
    expect(overview).not.toContain('.font(.title2)');
    // Service lifecycle stays on Proxies; Overview only reports health.
    expect(overview).not.toContain('Restart Running Proxies');
    expect(overview).not.toContain('Apps…');
    expect(overview).not.toContain('Accounts…');
    expect(overview).not.toMatch(/\broutes\b/i);
  });

  it('ships a System Settings Get Started checklist on Overview', () => {
    const overview = sources.get('OverviewView.swift') ?? '';
    const models = sources.get('Models.swift') ?? '';
    const panes = sources.get('SettingsPanes.swift') ?? '';
    const launcher = sources.get('AppEntry.swift') ?? '';

    expect(overview).toContain('Get Started');
    expect(overview).toContain('Save a Login');
    expect(overview).toContain('Use an App');
    expect(overview).toContain('Point an App');
    expect(overview).toContain('Set Up Proxy Hub');
    expect(overview).toContain('Button("Hide")');
    expect(overview).toContain('shouldShowOnboarding');
    expect(overview).toContain('Welcome to AnyPick');
    expect(overview).toContain('FirstRunWelcomeSheet');
    expect(overview).toContain('showsCancel: false');
    expect(models).toContain('model.showFirstRunWelcome =');
    expect(overview).not.toContain('NativeOnboardingChecklist');
    expect(overview).not.toContain('borderedProminent');
    expect(models).toContain('func dismissOnboarding()');
    expect(models).toContain('func showGettingStartedAgain()');
    expect(models).toContain('var shouldShowOnboarding');
    expect(models).toContain('ANYPICK_MACOS_FLOW"] == "getting-started"');
    expect(panes).toContain('Getting Started');
    expect(panes).toContain('Shown on Overview until setup is finished.');
    expect(launcher).not.toContain('onboardingDismissed');
  });

  it('splits large surfaces into smaller source files', () => {
    expect(names).toContain('DesignTypes.swift');
    expect(names).toContain('DesignFixture.swift');
    expect(names).toContain('AppDetailView.swift');
    expect(names).toContain('AppSheets.swift');
    expect(names).toContain('AccountDetailView.swift');
    expect(names).toContain('ProxyDetailView.swift');
    expect(names).toContain('SettingsPanes.swift');
    expect(names).toContain('MenuBarUseWith.swift');
    expect(names).toContain('MenuBarHome.swift');
    // Keep individual files focused — no 700+ line monsters in the UI shell.
    for (const name of ['MenuBarMenu.swift', 'SharedUI.swift', 'SettingsView.swift']) {
      const lines = (sources.get(name) ?? '').split('\n').length;
      expect(lines, name).toBeLessThan(450);
    }
  });

  it('launches only the UI fixture through the design launcher', async () => {
    const launcher = await readFile(LAUNCHER, 'utf8');

    expect(launcher).toMatch(/UI[- ]only/i);
    expect(launcher).toMatch(
      /readFileSync\s*\(\s*join\s*\(\s*prototypeDir,\s*['"]sources\.txt['"]/,
    );
    expect(launcher).toContain("valueAfter('--flow')");
    expect(launcher).toContain('ANYPICK_MACOS_FLOW');
    expect(launcher).toContain('tray-switching');
    expect(launcher).toContain('tray-recovery');
    expect(sources.get('MenuBarSwitching.swift') ?? '').toContain('TrayInlineSwitchState');
    expect(launcher).not.toMatch(/\bimport\s*\{[^}]*\bhomedir\b[^}]*\}\s*from\s*['"]node:os['"]/s);
    expect(launcher).not.toMatch(/\bfrom\s*['"][^'"]*(?:supervisor|demo-macos-tray)[^'"]*['"]/);
  });
});
