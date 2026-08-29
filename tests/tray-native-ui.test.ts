import { readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { beforeAll, describe, expect, it } from 'vitest';
import { readMacosSwiftBundle } from '../src/tray/native-sources';

function swiftDeclaration(source: string, declaration: string): string {
  const start = source.indexOf(declaration);
  if (start < 0) {
    throw new Error(`Missing Swift declaration: ${declaration}`);
  }

  const bodyStart = source.indexOf('{', start);
  if (bodyStart < 0) {
    throw new Error(`Missing body for Swift declaration: ${declaration}`);
  }

  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    if (source[index] === '{') {
      depth += 1;
    }
    if (source[index] === '}') {
      depth -= 1;
    }
    if (depth === 0) {
      return source.slice(start, index + 1);
    }
  }

  throw new Error(`Unclosed Swift declaration: ${declaration}`);
}

describe('macOS app surface', () => {
  let source: string;

  beforeAll(async () => {
    const macosDir = fileURLToPath(new URL('../src/macos/', import.meta.url));
    source = await readMacosSwiftBundle(macosDir);
  });

  it('launches as a full macOS app while keeping menu-bar quick access', async () => {
    const appDelegate = swiftDeclaration(source, 'final class AnyPickAppDelegate');
    const appScene = swiftDeclaration(source, 'struct AnyPickApp');
    const bundleBuilder = await readFile(
      fileURLToPath(new URL('../src/tray/supervisor-native.ts', import.meta.url)),
      'utf8',
    );
    expect(appDelegate).toContain('NSApp.setActivationPolicy(.regular)');
    expect(appDelegate).toContain('applicationShouldHandleReopen');
    expect(appDelegate).toContain('presentMainWindow()');
    expect(appDelegate).toContain('makeKeyAndOrderFront(nil)');
    expect(appDelegate).not.toContain('hideUnrequestedMainWindows');
    expect(appDelegate).toContain('let serviceSmoke =');
    expect(appDelegate).toContain('if serviceSmoke');
    expect(appDelegate).toContain('window.orderOut(nil)');
    expect(appDelegate).toContain('AnyPickAppIcon.applyToApp()');
    expect(appDelegate).not.toMatch(/NSWindowController|NSHostingController|NSWindow\s*\(/);
    expect(appScene).toContain('Window("AnyPick", id: "main")');
    expect(appScene).toContain('MainWindow(model: delegate.designModel)');
    expect(bundleBuilder).not.toContain('<key>LSUIElement</key>');
    expect(source).not.toContain('#if ANYPICK_DESIGN_FIXTURE');
    expect(source).not.toContain('struct NativeMainWindow');
    expect(source).not.toContain('struct NativeQuickPanel');
  });

  it('hosts the Power Quick Panel in the menu-bar extra', () => {
    const appScene = swiftDeclaration(source, 'struct AnyPickApp');
    const menuScene = swiftDeclaration(source, 'struct AnyPickMenuBarScene');
    const menuName = menuScene.match(/MenuBarExtra\s*\{\s*(\w+)\s*\(/)?.[1];

    expect(appScene).toContain('AnyPickMenuBarScene(model: delegate.designModel)');
    expect(menuScene).toContain('MenuBarExtra');
    expect(menuName).toBe('MenuBarMenu');
    expect(menuScene).toContain('.menuBarExtraStyle(.window)');
    expect(menuScene).not.toContain('.menuBarExtraStyle(.menu)');
    expect(menuScene).not.toContain('SidebarCommands()');
    const commands = swiftDeclaration(source, 'struct AppCommands');
    expect(commands).toContain('CommandMenu("Go")');
    expect(commands).toContain('DesignDestination.allCases');
    const menu = swiftDeclaration(source, 'struct MenuBarMenu');
    expect(menu).toContain('MenuBarProxiesRow');
    expect(menu).toContain('Open AnyPick');
    expect(menu).not.toContain('NativeQuickPanel');
  });

  it('opens management through the System Settings window scene', () => {
    const appDelegate = swiftDeclaration(source, 'final class AnyPickAppDelegate');
    const appScene = swiftDeclaration(source, 'struct AnyPickApp');
    const mainWindow = swiftDeclaration(source, 'struct MainWindow');

    expect(appDelegate).not.toMatch(/NSWindowController|NSHostingController/);
    expect(appScene).toContain('Window("AnyPick", id: "main")');
    expect(appScene).toContain('MainWindow(model: delegate.designModel)');
    expect(mainWindow).toContain('NavigationSplitView');
    expect(mainWindow).toContain('.listStyle(.sidebar)');
    expect(mainWindow).toContain('.searchable(text: $sidebarSearch, placement: .sidebar');
    expect(mainWindow).not.toContain('DesignSearchField');
    expect(mainWindow).not.toContain('.frame(width: 208)');
    expect(mainWindow).toContain('.followingDesignAppearance(model.appearance)');
    expect(mainWindow).toContain('case .backups: BackupsView(model: model)');
  });

  it('adapts the design model to the redacted production store', () => {
    const bridge = swiftDeclaration(source, 'final class ProductionDesignModel');
    const appDelegate = swiftDeclaration(source, 'final class AnyPickAppDelegate');

    expect(bridge).toContain('let store: TrayStore');
    expect(bridge).toContain('override var isProduction: Bool { true }');
    expect(bridge).toContain('store.accounts.map(designAccount)');
    expect(bridge).toContain('store.gatewayProviders.map');
    expect(bridge).toContain('override var currentLiveLogins');
    expect(bridge).toContain('$0.livePresent == true');
    expect(bridge).toContain('override func saveCurrentLogin');
    expect(bridge).toContain('operation: "account-save-current"');
    expect(bridge).toContain('operation: "gateway-test"');
    expect(bridge).toContain('override func cancelAccountLogin()');
    expect(source).toContain('emit("cancel\\t\\(data.base64EncodedString())")');
    expect(bridge).toContain('store.applyModelRoles');
    expect(bridge).toContain('store.inspectBackup(path: path)');
    expect(bridge).not.toContain('ANYPICK_HOME');
    expect(appDelegate).toContain('ProductionDesignModel(store: created)');
    expect(swiftDeclaration(source, 'struct AnyPickApp')).toContain(
      'MainWindow(model: delegate.designModel)',
    );
  });

  it('rejects oversized mutation protocol lines before claiming native busy state', () => {
    const sendMutation = swiftDeclaration(source, 'func sendMutationCommand<T: Encodable>');
    const commandIndex = sendMutation.indexOf('let command = "mutate\\t');
    const sizeCheckIndex = sendMutation.indexOf('command.lengthOfBytes(using: .utf8) <= 16 * 1024');
    const busyIndex = sendMutation.indexOf('claimBusy(requestId: requestId');

    expect(commandIndex).toBeGreaterThanOrEqual(0);
    expect(sizeCheckIndex).toBeGreaterThan(commandIndex);
    expect(busyIndex).toBeGreaterThan(sizeCheckIndex);
    expect(sendMutation).toContain('This request is too large to send.');
    expect(sendMutation).toContain('emit(command)');
    expect(sendMutation).not.toMatch(/emit\("mutate\\t/);
  });

  it('keeps Add Account detection optional before save', () => {
    const form = swiftDeclaration(source, 'struct AddSavedAccountSheet');
    expect(form).toContain('Check Again');
    expect(form).toContain('func detectLogin()');
    expect(form).not.toMatch(/\.disabled\([^\n]*!detected/);
  });

  it('ships monochrome brand icons for built-in providers including Kiro and Grok', async () => {
    const iconsDir = fileURLToPath(new URL('../src/tray/icons/', import.meta.url));
    const names = await readdir(iconsDir);
    for (const name of [
      'claude.svg',
      'openai.svg',
      'googlegemini.svg',
      'opencode.svg',
      'openrouter.svg',
      'kiro.svg',
      'grok.svg',
    ]) {
      expect(names, `missing tray brand icon ${name}`).toContain(name);
      const svg = await readFile(join(iconsDir, name), 'utf8');
      expect(svg).toMatch(/viewBox="0 0 24 24"/);
      expect(svg).toMatch(/fill="#fff"/);
    }
    const brands = swiftDeclaration(source, 'struct DesignProviderIcon');
    expect(brands).toContain('DesignBrandAsset.image(');
    expect(source).toContain('kiro-app.png');
    expect(source).toContain('grok-mark');
  });

  it('auto-polls logs from the store without leftover monitor chrome', () => {
    const store = swiftDeclaration(source, 'final class TrayStore');
    expect(store).toContain('startLogPolling');
    expect(store).toContain('stopLogPolling');
    expect(source).not.toContain('struct NativeMonitorPanel');
    expect(source).not.toContain('struct NativeLogViewer');
  });

  it('keeps backup import/export on the production model', () => {
    const bridge = swiftDeclaration(source, 'final class ProductionDesignModel');
    expect(bridge).toContain('NSSavePanel()');
    expect(bridge).toContain('NSOpenPanel()');
    expect(bridge).toContain('anyPickBackupType');
    expect(bridge).toContain('store.inspectBackup(path:');
    expect(source).toContain('struct BackupsView');
    expect(source).not.toContain('struct NativeDataPanel');
  });

  it('is split into modular Swift sources under src/macos', async () => {
    const macosDir = fileURLToPath(new URL('../src/macos/', import.meta.url));
    const names = (await readdir(macosDir)).filter((name) => name.endsWith('.swift'));
    expect(names.length).toBeGreaterThanOrEqual(8);
    expect(names).toContain('AppEntry.swift');
    expect(names).toContain('AppDelegate.swift');
    expect(names).toContain('MainWindow.swift');
    expect(names).toContain('Store.swift');
    expect(names).not.toContain('QuickPanel.swift');
  });
});
