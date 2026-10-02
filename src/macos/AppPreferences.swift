import Foundation
import CryptoKit

/// Local UI prefs (UserDefaults, not supervisor state).
enum AppPreferences {
  /// Production uses the standard domain. Tests swap in an isolated suite so
  /// a removal check cannot touch the installed app's preferences.
  static var defaults = UserDefaults.standard
  private static let prefix = "dev.anypick.tray."

  enum Key {
    static let onboardingDismissed = prefix + "onboardingDismissed"
    static let firstRunWelcomeSeen = prefix + "firstRunWelcomeSeen"
    static let appearance = prefix + "appearance"
    static let quickPanelHidden = prefix + "quickPanelHidden"
    static let quickPanelShown = prefix + "quickPanelShown"
    static let dismissedAttention = prefix + "dismissedAttention"
    static let configuredApps = prefix + "configuredApps"
    static let removedApps = prefix + "removedApps"
    static let codexAutoProfilesEnabled = prefix + "codexAutoProfilesEnabled"
    static let codexAutoProfileSources = prefix + "codexAutoProfileSources"
  }

  static var onboardingDismissed: Bool {
    get { defaults.bool(forKey: Key.onboardingDismissed) }
    set { defaults.set(newValue, forKey: Key.onboardingDismissed) }
  }

  static var firstRunWelcomeSeen: Bool {
    get { defaults.bool(forKey: Key.firstRunWelcomeSeen) }
    set { defaults.set(newValue, forKey: Key.firstRunWelcomeSeen) }
  }

  /// Persist a fixture/render appearance override. Product UI follows the system.
  static var appearanceRaw: String? {
    get { defaults.string(forKey: Key.appearance) }
    set { defaults.set(newValue, forKey: Key.appearance) }
  }

  /// Per-app Quick Panel visibility. `nil` means use the fixture/default.
  static func quickPanelShown(for appID: String) -> Bool? {
    if stringSet(Key.quickPanelHidden).contains(appID) { return false }
    if stringSet(Key.quickPanelShown).contains(appID) { return true }
    return nil
  }

  static var dismissedAttentionKeys: Set<String> {
    get { stringSet(Key.dismissedAttention) }
    set { defaults.set(Array(newValue), forKey: Key.dismissedAttention) }
  }

  static func setQuickPanelShown(_ shows: Bool, for appID: String) {
    var hidden = stringSet(Key.quickPanelHidden)
    var shown = stringSet(Key.quickPanelShown)
    if shows {
      hidden.remove(appID)
      shown.insert(appID)
    } else {
      shown.remove(appID)
      hidden.insert(appID)
    }
    defaults.set(Array(hidden), forKey: Key.quickPanelHidden)
    defaults.set(Array(shown), forKey: Key.quickPanelShown)
  }

  static var removedAppIDs: Set<String> {
    get { stringSet(Key.removedApps) }
    set { defaults.set(Array(newValue), forKey: Key.removedApps) }
  }

  static var configuredAppIDs: Set<String>? {
    get {
      guard let array = defaults.stringArray(forKey: Key.configuredApps) else { return nil }
      return Set(array)
    }
    set {
      if let newValue {
        defaults.set(Array(newValue), forKey: Key.configuredApps)
      } else {
        defaults.removeObject(forKey: Key.configuredApps)
      }
    }
  }

  static var codexAutoProfilesEnabled: Bool {
    get {
      guard defaults.object(forKey: Key.codexAutoProfilesEnabled) != nil else { return true }
      return defaults.bool(forKey: Key.codexAutoProfilesEnabled)
    }
    set { defaults.set(newValue, forKey: Key.codexAutoProfilesEnabled) }
  }

  /// Source refs are identifiers, so persist only one-way fingerprints in UI preferences.
  static var codexAutoProfileSourceFingerprints: Set<String> {
    get { stringSet(Key.codexAutoProfileSources) }
    set { defaults.set(Array(newValue), forKey: Key.codexAutoProfileSources) }
  }

  static func codexAutoProfileFingerprint(for sourceID: String) -> String {
    SHA256.hash(data: Data(sourceID.utf8))
      .map { String(format: "%02x", Int($0)) }
      .joined()
  }

  static func addConfiguredApp(_ appID: String) {
    var removed = removedAppIDs
    removed.remove(appID)
    removedAppIDs = removed

    var configured = configuredAppIDs ?? Set<String>()
    configured.insert(appID)
    configuredAppIDs = configured
  }

  static func removeApp(_ appID: String) {
    var removed = removedAppIDs
    removed.insert(appID)
    removedAppIDs = removed

    if var configured = configuredAppIDs {
      configured.remove(appID)
      configuredAppIDs = configured
    }
    setQuickPanelShown(false, for: appID)
  }

  static func isAppRemoved(_ appID: String) -> Bool {
    removedAppIDs.contains(appID)
  }

  static func effectiveConfiguredAppIDs(allApps: [DesignApp]) -> Set<String> {
    let removed = removedAppIDs
    if let configured = configuredAppIDs {
      return configured.subtracting(removed)
    }
    // Initial run: only include apps actively managed or configured/detected.
    var initial = Set<String>()
    for app in allApps {
      if isAppManagedByAnyPick(app) || isAppRouteConfigured(app) || app.status == .ready {
        initial.insert(app.id)
      }
    }
    if initial.isEmpty {
      for id in ["claude", "codex", "kiro"] {
        if allApps.contains(where: { $0.id == id }) {
          initial.insert(id)
        }
      }
    }
    initial = initial.subtracting(removed)
    configuredAppIDs = initial
    return initial
  }

  private static func stringSet(_ key: String) -> Set<String> {
    Set(defaults.stringArray(forKey: key) ?? [])
  }

  static func resetOnboarding() {
    onboardingDismissed = false
  }

  static func resetOnboardingIfEmpty(isFirstRun: Bool) {
    if isFirstRun { onboardingDismissed = false }
  }
}

enum TrayPreferences {
  static var onboardingDismissed: Bool {
    get { AppPreferences.onboardingDismissed }
    set { AppPreferences.onboardingDismissed = newValue }
  }

  static var firstRunWelcomeSeen: Bool {
    get { AppPreferences.firstRunWelcomeSeen }
    set { AppPreferences.firstRunWelcomeSeen = newValue }
  }

  static var appearance: DesignAppearance {
    get { DesignAppearance(rawValue: AppPreferences.appearanceRaw ?? "") ?? .system }
    set { AppPreferences.appearanceRaw = newValue.rawValue }
  }

  static func resetOnboardingIfEmpty(isFirstRun: Bool) {
    AppPreferences.resetOnboardingIfEmpty(isFirstRun: isFirstRun)
  }
}
