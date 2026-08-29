import Foundation

/// Local UI prefs (UserDefaults, not supervisor state).
enum AppPreferences {
  private static let defaults = UserDefaults.standard
  private static let prefix = "dev.anypick.tray."

  enum Key {
    static let onboardingDismissed = prefix + "onboardingDismissed"
    static let firstRunWelcomeSeen = prefix + "firstRunWelcomeSeen"
    static let appearance = prefix + "appearance"
    static let quickPanelHidden = prefix + "quickPanelHidden"
    static let quickPanelShown = prefix + "quickPanelShown"
    static let dismissedAttention = prefix + "dismissedAttention"
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
