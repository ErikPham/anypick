import AppKit
import SwiftUI

/// Provider / app identity. Chrome uses SF Symbols; only this view loads brand files.
struct DesignProviderIcon: View {
  let provider: String
  var fallbackSymbol = "app.fill"
  var size: CGFloat = 22

  @Environment(\.colorScheme) private var colorScheme

  var body: some View {
    Group {
      if let image = DesignBrandAsset.image(
        for: provider,
        pointSize: size,
        dark: colorScheme == .dark
      ) {
        Image(nsImage: image)
          .renderingMode(image.isTemplate ? .template : .original)
          .resizable()
          .interpolation(.high)
          .scaledToFit()
          .foregroundStyle(.primary)
          .frame(width: size, height: size)
      } else {
        Image(systemName: fallbackSymbol)
          .symbolRenderingMode(.hierarchical)
          .foregroundStyle(.secondary)
          .frame(width: size, height: size)
      }
    }
    .accessibilityHidden(true)
  }
}

/// Native provider pop-up. SwiftUI's Picker drops custom image labels from
/// NSMenu items, so bridge the platform control when brand identity matters.
struct DesignProviderPicker: NSViewRepresentable {
  let options: [DesignProviderOption]
  @Binding var selection: String
  var iconSize: CGFloat = 16
  var accessibilityIdentifier = "account-flow.provider"
  var groupsProviders = false

  func makeCoordinator() -> Coordinator {
    Coordinator(selection: $selection)
  }

  func makeNSView(context: Context) -> NSPopUpButton {
    let button = NSPopUpButton(frame: .zero, pullsDown: false)
    button.controlSize = .small
    button.autoenablesItems = false
    button.setContentHuggingPriority(.required, for: .horizontal)
    button.setContentCompressionResistancePriority(.required, for: .horizontal)
    button.target = context.coordinator
    button.action = #selector(Coordinator.selectProvider(_:))
    button.setAccessibilityIdentifier(accessibilityIdentifier)
    return button
  }

  func updateNSView(_ button: NSPopUpButton, context: Context) {
    context.coordinator.selection = $selection
    button.removeAllItems()
    let dark = button.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
    if groupsProviders {
      appendGroup(
        title: "Hosted",
        options: options.filter { $0.kind != "custom" },
        to: button,
        dark: dark
      )
      let custom = options.filter { $0.kind == "custom" }
      if !custom.isEmpty {
        if button.numberOfItems > 0 { button.menu?.addItem(.separator()) }
        appendGroup(title: "Self-Hosted & Custom", options: custom, to: button, dark: dark)
      }
    } else {
      append(options: options, to: button, dark: dark)
    }
    if let item = button.itemArray.first(where: {
      ($0.representedObject as? String) == selection
    }) {
      button.select(item)
    }
    button.sizeToFit()
  }

  private func appendGroup(
    title: String,
    options: [DesignProviderOption],
    to button: NSPopUpButton,
    dark: Bool
  ) {
    guard !options.isEmpty else { return }
    let heading = NSMenuItem(title: title, action: nil, keyEquivalent: "")
    heading.isEnabled = false
    button.menu?.addItem(heading)
    append(options: options, to: button, dark: dark)
  }

  private func append(
    options: [DesignProviderOption],
    to button: NSPopUpButton,
    dark: Bool
  ) {
    for option in options {
      let item = NSMenuItem(title: option.label, action: nil, keyEquivalent: "")
      item.representedObject = option.id
      item.toolTip = option.detail
      item.isEnabled = option.enabled
      if let image = DesignBrandAsset.image(for: option.id, pointSize: iconSize, dark: dark) {
        image.size = NSSize(width: iconSize, height: iconSize)
        item.image = image
      } else if let symbol = NSImage(
        systemSymbolName: DesignBrandAsset.fallbackSymbol(for: option.id),
        accessibilityDescription: option.label
      ) {
        // Vendors without a bundled mark (compatible API providers and custom
        // gateways) get the same monochrome glyph the SwiftUI rows use.
        // A template symbol follows the menu label color, so disabled rows
        // dim correctly instead of rendering a dark tile.
        symbol.size = NSSize(width: iconSize, height: iconSize)
        symbol.isTemplate = true
        item.image = symbol
      }
      button.menu?.addItem(item)
    }
  }

  final class Coordinator: NSObject {
    var selection: Binding<String>

    init(selection: Binding<String>) {
      self.selection = selection
    }

    @objc func selectProvider(_ sender: NSPopUpButton) {
      guard let id = sender.selectedItem?.representedObject as? String else { return }
      selection.wrappedValue = id
    }
  }
}

extension DesignBrandAsset {
  static func image(for provider: String, pointSize: CGFloat, dark: Bool) -> NSImage? {
    image(named: candidateFiles(for: provider, dark: dark), pointSize: pointSize)
  }

  private static func candidateFiles(for provider: String, dark: Bool) -> [String] {
    switch normalize(provider) {
    case "claude", "anthropic", "claudecode":
      // Orange burst first (brand color, both appearances); the white mark
      // stays as fallback.
      return ["claude-mark-orange.svg", "claude-mark.png", "claude.svg"]
    case "codex", "codexcli":
      return dark
        ? ["codex-app-dark.png", "codex-app-light.png", "openai.svg"]
        : ["codex-app-light.png", "codex-app-dark.png", "openai.svg"]
    case "openai":
      return dark
        ? ["openai-mark-dark.png", "openai-mark-light.png", "openai.svg"]
        : ["openai-mark-light.png", "openai-mark-dark.png", "openai.svg"]
    case "gemini", "google", "geminicli", "antigravity", "googleaipro":
      // Official 2025 icon (blue→lavender gradient) first; the white
      // single-fill mark stays as fallback.
      return ["gemini-icon-2025.svg", "gemini-mark-v2.png", "gemini-mark.png", "googlegemini.svg"]
    case "kiro", "personalbuilder":
      return ["kiro-app.png", "kiro.svg"]
    case "workbuddy", "codebuddy":
      // Full-color app icon (workbuddy-mark.png). Same convention as
      // kiro-app.png: the row shows the app's own icon to identify which
      // client is being configured.
      return ["workbuddy-mark.png", "workbuddy.svg"]
    case "openrouter":
      return dark
        ? ["openrouter-glyph-dark.png", "openrouter-glyph-light.png", "openrouter.svg"]
        : ["openrouter-glyph-light.png", "openrouter-glyph-dark.png", "openrouter.svg"]
    case "grok", "xai":
      // The filled light star reads as a black tile at list size; the XI
      // monogram stays legible in both appearances.
      return ["grok-mark-dark.png", "grok-mark-light.png", "grok.svg"]
    case "opencode":
      return dark
        ? ["opencode-mark-dark.png", "opencode-mark-light.png", "opencode.svg"]
        : ["opencode-mark-light.png", "opencode-mark-dark.png", "opencode.svg"]
    case "opencodezen", "opencodego":
      // Zen/Go rows share OpenCode's own mark: they are OpenCode products,
      // not third-party vendors.
      return ["opencode.svg"]
    case "deepseek":
      return ["deepseek.svg"]
    case "kimi", "moonshot":
      return ["kimi.svg"]
    case "zai", "glm":
      return ["zai.svg"]
    case "qwen", "dashscope":
      return ["qwen.svg"]
    case "mistral":
      return ["mistral.svg"]
    case "mimo", "xiaomi":
      return ["mimo.svg"]
    case "minimax":
      return ["minimax.svg"]
    case "doubao", "bytedance":
      // Doubao is ByteDance's AI app; the company mark identifies the row.
      return ["doubao.svg"]
    case "hunyuan":
      return ["hunyuan.svg"]
    case "modelscope":
      return ["modelscope.svg"]
    case "nvidia", "nvidianim":
      return ["nvidia.svg"]
    case "zcode":
      // Full-color app icon, same convention as kiro-app.png and
      // workbuddy-mark.png: the row shows the app's own icon so the user can
      // tell which client is being configured. Deliberately does not cover
      // "zai" — that is Z.ai's API-key provider, and the ZCode app icon is not
      // the Z.ai logo, so an account row would be wearing the wrong brand.
      return ["zcode-app.png"]
    case "dsh":
      // DeepSeek's harness CLI: no mark of its own, wears the DeepSeek brand.
      return ["deepseek.svg"]
    default:
      return []
    }
  }
}

struct DesignProductMark: View {
  var size: CGFloat = 28

  var body: some View {
    Group {
      if let image = DesignBrandAsset.image(
        named: ["app-icon-32.png", "app-icon-64.png", "app-icon-128.png"],
        pointSize: size
      ) {
        Image(nsImage: image)
          .resizable()
          .interpolation(.high)
          .scaledToFit()
          .frame(width: size, height: size)
      } else {
        Image(systemName: "app.fill")
          .symbolRenderingMode(.hierarchical)
          .foregroundStyle(.secondary)
          .frame(width: size, height: size)
      }
    }
    .accessibilityHidden(true)
  }
}

enum DesignBrandAsset {
  static func fallbackSymbol(for provider: String) -> String {
    switch normalize(provider) {
    case "claude", "anthropic", "claudecode": "brain.head.profile"
    case "codex", "codexcli", "openai": "terminal.fill"
    case "gemini", "google", "geminicli", "antigravity", "googleaipro": "sparkle"
    case "kiro", "personalbuilder": "shippingbox.fill"
    case "openrouter": "network"
    case "grok", "xai": "sparkles"
    case "opencode": "chevron.left.forwardslash.chevron.right"
    // Full-color app icons ship in Resources/icons; anything else here is a
    // monochrome mark, so an unbundled row degrades to the same glyph.
    case "zcode": "curlybraces"
    // Briefcase only when the bundled workbuddy-mark.png is absent.
    case "workbuddy", "codebuddy": "briefcase.fill"
    default: "server.rack"
    }
  }

  static func image(named names: [String], pointSize: CGFloat) -> NSImage? {
    for name in names {
      for url in searchURLs(name) {
        if let image = NSImage(contentsOf: url) {
          image.size = NSSize(width: pointSize * 2, height: pointSize * 2)
          image.isTemplate = isTemplateMark(name)
          return image
        }
      }
    }
    return nil
  }

  /// Monochrome glyphs follow the label color. Full-color product marks stay original.
  /// Only the files listed render original: the rest of the SVGs are white or
  /// black single-fill marks that must template-tint per appearance instead
  /// of showing their raw fill (white-on-light is invisible, black-on-dark
  /// is mud). Keep this set in sync with Resources/icons: a new chromatic
  /// mark renders black in Light Mode until it is listed here.
  private static let fullColorMarks: Set<String> = [
    "claude-mark-orange.svg", "deepseek.svg", "doubao.svg",
    "gemini-icon-2025.svg", "hunyuan.svg", "mimo.svg", "minimax.svg",
    "mistral.svg", "modelscope.svg", "nvidia.svg", "qwen.svg",
  ]

  /// Render policy for a brand file, pinned by tests: full-color marks stay
  /// original in both appearances; monochrome single-fill marks template-tint
  /// per appearance.
  static func rendersOriginal(_ file: String) -> Bool {
    let lower = file.lowercased()
    if fullColorMarks.contains(lower) { return true }
    if lower.hasSuffix(".svg") { return false }
    if lower.hasPrefix("grok-mark")
      || lower.hasPrefix("openai-mark")
      || lower.hasPrefix("opencode-mark")
    {
      return false
    }
    return true
  }

  private static func isTemplateMark(_ name: String) -> Bool {
    !rendersOriginal(name)
  }

  static func normalize(_ value: String) -> String {
    let provider = value.split(separator: ":", maxSplits: 1).first.map(String.init) ?? value
    return provider.lowercased()
      .replacingOccurrences(of: " ", with: "")
      .replacingOccurrences(of: "-", with: "")
      .replacingOccurrences(of: "_", with: "")
  }

  private static func searchURLs(_ file: String) -> [URL] {
    var urls: [URL] = []
    if let directory = ProcessInfo.processInfo.environment["ANYPICK_TRAY_ICON_DIR"] {
      urls.append(URL(fileURLWithPath: directory).appendingPathComponent(file))
    }
    if let resource = Bundle.main.resourceURL {
      urls.append(resource.appendingPathComponent("icons/\(file)"))
      urls.append(resource.appendingPathComponent("brands/\(file)"))
      urls.append(resource.appendingPathComponent("anypick/\(file)"))
      urls.append(resource.appendingPathComponent(file))
    }
    if let exeDir = Bundle.main.executableURL?.deletingLastPathComponent() {
      let resources = exeDir.deletingLastPathComponent().appendingPathComponent("Resources")
      urls.append(resources.appendingPathComponent("icons/\(file)"))
      urls.append(exeDir.appendingPathComponent("icons/\(file)"))
    }
    let roots = [
      FileManager.default.currentDirectoryPath,
      ProcessInfo.processInfo.environment["PWD"],
    ].compactMap { $0 }
    for root in roots {
      urls.append(URL(fileURLWithPath: root).appendingPathComponent("design/assets/brands/\(file)"))
      urls.append(
        URL(fileURLWithPath: root).appendingPathComponent("design/assets/anypick/\(file)"))
      urls.append(URL(fileURLWithPath: root).appendingPathComponent("src/tray/icons/\(file)"))
    }
    return urls
  }
}
