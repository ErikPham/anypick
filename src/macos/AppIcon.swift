import AppKit

/// Dock / Cmd-Tab app icon. Prefers bundle Resources, then a generated mark.
enum AnyPickAppIcon {
  static let image: NSImage = {
    if let bundled = NSImage(named: "AppIcon") { return bundled }
    if let url = Bundle.main.url(forResource: "AppIcon", withExtension: "icns")
      ?? Bundle.main.url(forResource: "AppIcon", withExtension: "png"),
      let image = NSImage(contentsOf: url)
    {
      return image
    }
    if let exeDir = Bundle.main.executableURL?.deletingLastPathComponent() {
      let resources = exeDir.deletingLastPathComponent().appendingPathComponent("Resources")
      for file in ["AppIcon.icns", "AppIcon.png"] {
        let url = resources.appendingPathComponent(file)
        if let image = NSImage(contentsOf: url) { return image }
      }
    }
    return generatedColorMark(size: 256)
  }()

  static func applyToApp() {
    NSApp.applicationIconImage = image
  }

  private static func generatedColorMark(size: CGFloat) -> NSImage {
    let image = NSImage(size: NSSize(width: size, height: size), flipped: true) { rect in
      let background = NSBezierPath(
        roundedRect: rect,
        xRadius: size * 14 / 64,
        yRadius: size * 14 / 64
      )
      NSGradient(
        starting: NSColor(red: 139 / 255, green: 92 / 255, blue: 246 / 255, alpha: 1),
        ending: NSColor(red: 109 / 255, green: 40 / 255, blue: 217 / 255, alpha: 1)
      )?.draw(in: background, angle: -45)

      let scale = size * 0.75 / 64
      let offset = size * 0.125
      func point(_ x: CGFloat, _ y: CGFloat) -> NSPoint {
        NSPoint(x: offset + x * scale, y: offset + y * scale)
      }
      func circle(_ x: CGFloat, _ y: CGFloat, _ radius: CGFloat) -> NSRect {
        NSRect(
          x: offset + (x - radius) * scale,
          y: offset + (y - radius) * scale,
          width: radius * 2 * scale,
          height: radius * 2 * scale
        )
      }
      let branches = NSBezierPath()
      branches.move(to: point(14, 14))
      branches.line(to: point(23, 14))
      branches.line(to: point(36, 24))
      branches.move(to: point(14, 32))
      branches.line(to: point(29, 32))
      branches.move(to: point(14, 50))
      branches.line(to: point(23, 50))
      branches.line(to: point(36, 40))
      branches.move(to: point(30, 14))
      branches.line(to: point(50, 32))
      branches.line(to: point(30, 50))
      branches.lineWidth = 7.5 * scale
      branches.lineCapStyle = .round
      branches.lineJoinStyle = .round
      NSColor.white.setFill()
      NSColor.white.setStroke()
      branches.stroke()
      for (x, y, radius) in [(10, 14, 5.6), (10, 32, 5.6), (10, 50, 5.6)] {
        NSBezierPath(ovalIn: circle(CGFloat(x), CGFloat(y), CGFloat(radius))).fill()
      }
      return true
    }
    image.accessibilityDescription = "AnyPick"
    return image
  }
}

func anypickDisplayName(_ client: String) -> String {
  switch client.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() {
  case "claude", "claude-code": return "Claude Code"
  case "codex": return "Codex"
  case "gemini", "gemini-cli": return "Gemini"
  case "kiro": return "Kiro"
  // The same product ships as WorkBuddy outside mainland China and CodeBuddy
  // inside it; AnyPick keys everything on "workbuddy" and treats the other as
  // an alias so either install resolves to one row.
  case "workbuddy", "codebuddy": return "WorkBuddy"
  default: return client
  }
}

func nativeProviderName(_ providerId: String) -> String {
  switch nativeProviderFamily(providerId) {
  case "claude": return "Claude"
  case "openai": return "OpenAI"
  case "gemini": return "Google Gemini"
  case "kiro": return "Kiro"
  case "openrouter": return "OpenRouter"
  case "opencode": return "OpenCode"
  case "grok": return "Grok"
  case "workbuddy", "codebuddy": return "WorkBuddy"
  case "proxy-hub": return "Proxy Hub"
  default:
    return providerId
      .split(separator: "-")
      .map { $0.prefix(1).uppercased() + $0.dropFirst() }
      .joined(separator: " ")
  }
}

func nativeProviderFamily(_ providerId: String) -> String {
  let normalized = providerId.lowercased()
    .split(whereSeparator: { $0 == "/" || $0 == ":" })
    .first
    .map(String.init) ?? providerId.lowercased()
  switch normalized {
  case "anthropic", "claude", "claude-code": return "claude"
  case "codex", "openai": return "openai"
  case "gemini", "gemini-cli", "antigravity": return "gemini"
  case "grok", "xai", "x-ai": return "grok"
  case "proxy-hub", "hub": return "proxy-hub"
  default: return normalized
  }
}
