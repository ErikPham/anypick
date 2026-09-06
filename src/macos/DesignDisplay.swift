import Foundation

// Shared display helpers for macOS panes and Quick Panel (P1-01…P1-04).
// Centralizes route canonicalization, identity formatting, and friendly
// copy so planner and executor cannot diverge on ad-hoc strings.
// Also hosts the add-account sheet gates (AUDIT_2026-09-03 C2/C6/C8/C9/C10):
// this file compiles into AnyPickTests, so the gates stay pinned without
// hosting SwiftUI.

/// Canonical key for a route: provider family + account identity.
/// Two actions pointing at the same saved login (different engine IDs)
/// collapse to one row; the current route is never shown twice.
func routeDedupeKey(_ route: DesignRoute) -> String {
  let provider = (route.providerID ?? "").lowercased().trimmingCharacters(in: .whitespacesAndNewlines)
  let name = route.name.lowercased().trimmingCharacters(in: .whitespacesAndNewlines)
  let detail = route.detail.lowercased().trimmingCharacters(in: .whitespacesAndNewlines)
  return "\(provider)|\(name)|\(detail)"
}

/// Sort native first, then canonicalize/dedupe. When duplicates share a key,
/// the selected route wins so the current route appears exactly once.
func uniqueCompatibleRoutes(_ routes: [DesignRoute], selectedID: String?) -> [DesignRoute] {
  let sorted = nativeFirstRoutes(routes)
  var byKey: [String: DesignRoute] = [:]
  var order: [String] = []
  for route in sorted {
    let key = routeDedupeKey(route)
    if let existing = byKey[key] {
      if route.id == selectedID, existing.id != selectedID {
        byKey[key] = route
      }
      continue
    }
    byKey[key] = route
    order.append(key)
  }
  return order.compactMap { byKey[$0] }
}

/// Account + gateway routes for one app, deduped. Single source of truth
/// for the switcher, AppRouteSheet, and Quick Panel counts.
func compatibleAccountRoutes(in app: DesignApp) -> [DesignRoute] {
  uniqueCompatibleRoutes(
    app.routeOptions.filter { $0.kind == .account || $0.kind == .native },
    selectedID: app.selectedRouteID.isEmpty ? nil : app.selectedRouteID
  )
}

/// Join provider and identity without a dangling separator (P1-02).
func accountIdentityLine(provider: String, detail: String) -> String {
  let cleanProvider = provider.trimmingCharacters(in: .whitespacesAndNewlines)
  let cleanDetail = detail.trimmingCharacters(in: .whitespacesAndNewlines)
  switch (cleanProvider.isEmpty, cleanDetail.isEmpty) {
  case (true, true): return ""
  case (true, false): return cleanDetail
  case (false, true): return cleanProvider
  case (false, false): return "\(cleanProvider) · \(cleanDetail)"
  }
}

/// Visible name that stays distinguishable when two accounts share a label.
/// Appends the provider only on collision so unique names stay clean.
func disambiguatedAccountName(for account: DesignAccount, in all: [DesignAccount]) -> String {
  let collisions = all.filter {
    $0.id != account.id
      && $0.name.caseInsensitiveCompare(account.name) == .orderedSame
  }
  guard !collisions.isEmpty else { return account.name }
  if collisions.allSatisfy({ $0.provider.caseInsensitiveCompare(account.provider) == .orderedSame }) {
    let detail = account.detail.trimmingCharacters(in: .whitespacesAndNewlines)
    return detail.isEmpty ? account.name : "\(account.name) · \(detail)"
  }
  return "\(account.name) (\(account.provider))"
}

/// VoiceOver label: account, provider, identity, plus position on collision.
func accountAccessibilityLabel(for account: DesignAccount, in all: [DesignAccount]) -> String {
  let identity = accountIdentityLine(provider: account.provider, detail: account.detail)
  let base: String
  if identity.isEmpty || identity == account.name {
    base = "\(account.name), \(account.provider)"
  } else if account.name.isEmpty {
    base = identity
  } else {
    base = "\(account.name), \(identity)"
  }
  let sameName = all.filter {
    $0.name.caseInsensitiveCompare(account.name) == .orderedSame
  }.sorted {
    $0.id.localizedStandardCompare($1.id) == .orderedAscending
  }
  guard sameName.count > 1, let index = sameName.firstIndex(where: { $0.id == account.id }) else {
    return base
  }
  return "\(base) (\(index + 1) of \(sameName.count))"
}

/// Map a raw engine routeKind / detail to user-facing copy (P1-03).
/// Never surfaces "direct-account", "account", or "provider/id proxy".
func friendlyRouteDetail(_ rawDetail: String?, routeKind: String?) -> String {
  let cleaned = (rawDetail ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
  let rawKind = (routeKind ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
  let looksInternal = cleaned.isEmpty
    || cleaned == rawKind
    || cleaned == "direct-account"
    || cleaned == "account"
    || cleaned == "pool"
  if !looksInternal { return cleaned }
  switch rawKind {
  case "direct-account": return "Direct account"
  case "account", "pool": return "Saved account"
  case "hub": return "Proxy Hub"
  case "gateway": return "API gateway"
  case "native": return "Direct account"
  default: return "Available route"
  }
}

func friendlyRouteKindLabel(_ kind: DesignRouteKind) -> String {
  switch kind {
  case .native: return "Direct account"
  case .account: return "Saved account"
  case .hub: return "Proxy Hub"
  case .gateway: return "API gateway"
  }
}

/// Engine proxy labels can be "provider/internal-id proxy".
/// Prefer the friendly provider name so no composite ID reaches the UI.
func friendlyProxyName(label: String, providerId: String) -> String {
  let cleaned = label.trimmingCharacters(in: .whitespacesAndNewlines)
  if cleaned.contains("/") {
    return "\(nativeProviderName(providerId)) proxy"
  }
  if cleaned.isEmpty { return "\(nativeProviderName(providerId)) proxy" }
  return cleaned
}

/// Parse a loopback service address to its port. URL parsing needs a
/// scheme, so schemeless "127.0.0.1:41401" (dynamic assignment, H6) falls
/// back to a host:port split. Bracketed IPv6 ("[::1]:5150") works too.
func servicePort(from address: String?) -> Int? {
  guard let address = address?.trimmingCharacters(in: .whitespacesAndNewlines),
    !address.isEmpty
  else { return nil }
  if let port = URL(string: address)?.port { return port }
  let hostPort: String
  if let schemeRange = address.range(of: "://") {
    hostPort = String(address[schemeRange.upperBound...])
  } else {
    hostPort = address
  }
  guard let colon = hostPort.range(of: ":", options: .backwards) else { return nil }
  let portText = String(hostPort[colon.upperBound...])
  guard !portText.isEmpty, !portText.contains("/") else { return nil }
  return Int(portText)
}

/// Stable service-operation keys so ProxyHubView rows and ProductionModel
/// agree without string surgery (H7).
func hubSourceServiceID(_ accountID: String) -> String { "hub-source/\(accountID)" }
func hubConflictServiceID(_ conflictID: String) -> String { "hub-conflict/\(conflictID)" }

/// Vendor window cadence as a duration noun (Usage): 18000 → "5 hours",
/// 604800 → "week". Data-driven only — nil when unknown. Callers prefix
/// "every"/"Every"; nothing here names a plan ("5-hour" is not a name the
/// payload gives us).
func usageWindowPeriod(limitWindowSeconds: Int?) -> String? {
  guard let seconds = limitWindowSeconds, seconds > 0 else { return nil }
  if seconds < 3600 {
    let mins = max(1, Int((Double(seconds) / 60).rounded()))
    return mins == 1 ? "1 minute" : "\(mins) minutes"
  }
  let hours = seconds / 3600
  if hours < 24 {
    return hours == 1 ? "1 hour" : "\(hours) hours"
  }
  let days = seconds / 86400
  if days == 1 { return "day" }
  if days == 7 { return "week" }
  if days % 7 == 0 { return "\(days / 7) weeks" }
  return "\(days) days"
}

/// Lines surviving a log Clear (L5). The clear hides by count, which a file
/// rotation or restart invalidates: when the tail is strictly shorter than
/// what was hidden, the old count resets — otherwise new entries stay hidden
/// forever. Equal counts keep hiding (same tail, still cleared); a
/// same-length restart still hides, and only a byte offset would catch that.
func visibleLogLines(_ lines: [String], hiddenCount: inout Int, limit: Int = 160) -> [String] {
  if lines.count < hiddenCount { hiddenCount = 0 }
  return Array(lines.dropFirst(min(hiddenCount, lines.count)).suffix(limit))
}

// MARK: - Backups menu gates (AUDIT_2026-09-03 B7)

/// Entry IDs to import (B7). Without overwrite the engine skips `replace`
/// dispositions, so an all-skipped selection must warn instead of sitting
/// open with no feedback. Empty means "nothing would change".
func backupImportEntryIDs(
  selectedIDs: Set<String>,
  dispositions: [String: String],
  overwrite: Bool
) -> [String] {
  selectedIDs.filter { id in
    overwrite || dispositions[id] != "replace"
  }.sorted()
}

// MARK: - Overview menu gates (AUDIT_2026-09-03 O1/O2/O3/O4/O7)

/// Saved logins only — API gateways are not logins (O2). A lone gateway used
/// to mark "Save a Login" Done and hide the checklist early.
func overviewHasSavedLogin(accounts: [DesignAccount]) -> Bool {
  accounts.contains { $0.kind == .saved }
}

/// One app counts as pointed when its selected route is a managed,
/// non-native route (O3): a native-only login is setup still to do, and a
/// stale selection missing from the options counts for nothing.
func overviewIsChosenRoute(_ app: DesignApp) -> Bool {
  guard let selected = app.routeOptions.first(where: { $0.id == app.selectedRouteID }) else {
    return false
  }
  return selected.kind != .native
}

/// Checklist predicate shared by fixture and production (O3): both read the
/// snapshot-derived apps, so a native-only login is Not done everywhere.
func overviewHasChosenRoute(apps: [DesignApp]) -> Bool {
  apps.contains(where: overviewIsChosenRoute)
}

/// Primary attention target, proxies → apps → accounts → conflicts (O1:
/// saved-account attention used to fall through silently even though
/// attentionCount includes it).
enum OverviewAttentionTarget: Equatable {
  case proxy(id: String)
  case app(id: String)
  case account(id: String)
  case conflict(kind: DesignRoutingConflictKind)
}

func overviewAttentionTarget(
  proxies: [DesignProxy],
  apps: [DesignApp],
  accounts: [DesignAccount],
  conflicts: [DesignRoutingConflict]
) -> OverviewAttentionTarget? {
  if let proxy = proxies.first(where: {
    $0.status == .attention || $0.status == .unavailable
  }) {
    return .proxy(id: proxy.id)
  }
  if let app = apps.first(where: {
    $0.status == .attention || $0.status == .unavailable
  }) {
    return .app(id: app.id)
  }
  if let account = accounts.first(where: {
    $0.status == .attention || $0.status == .unavailable
  }) {
    return .account(id: account.id)
  }
  if conflicts.contains(where: { $0.kind == .sourceChoice }) {
    return .conflict(kind: .sourceChoice)
  }
  if conflicts.contains(where: { $0.kind == .modelOverlap }) {
    return .conflict(kind: .modelOverlap)
  }
  return nil
}

/// Services total (O4): the Hub slot counts only when a hub is configured —
/// sources listed or running — never a phantom +1 ("0 of 1 running").
func overviewServiceTotal(proxyCount: Int, hubSourcesEmpty: Bool, hubEnabled: Bool) -> Int {
  proxyCount + (hubSourcesEmpty && !hubEnabled ? 0 : 1)
}

/// Row summary keeps production detail (O7): the engine's connectionDetail
/// ("Signed in as…", "Configured source unavailable") survives instead of a
/// flat "No login chosen".
func overviewConnectionSummary(_ app: DesignApp) -> String {
  if let name = app.routeOptions.first(where: { $0.id == app.selectedRouteID })?.name {
    return "Uses \(name)"
  }
  return app.detail.isEmpty ? "No login chosen" : app.detail
}

// MARK: - Proxies menu gates (AUDIT_2026-09-03 PX3)

/// Restart availability mirrors the Proxies list row (PX3): enabled services
/// restart, and so do stopped ones needing attention. The Detail used to
/// disable Restart whenever the proxy was not enabled — a different rule
/// from the list row for the same service.
func canRestartProxy(enabled: Bool, status: DesignStatus) -> Bool {
  enabled || status == .attention || status == .unavailable
}

// MARK: - Tray menu gates (AUDIT_2026-09-03 T1/T3/T4/T6)

/// Dismissal primitives for attention notices (T3). Only string IDs cross
/// into the test bundle: the item type lives with the tray chrome, while the
/// key format, legacy compat, and pruning are pinned here. The key is the
/// stable item id — legacy "id|title" keys (any title) still count as
/// dismissed so a copy change cannot resurrect old notices, but new
/// dismissals store the id only.
func trayAttentionDismissed(id: String, title: String, keys: Set<String>) -> Bool {
  keys.contains(id) || keys.contains("\(id)|\(title)")
    || keys.contains(where: { $0.hasPrefix("\(id)|") })
}

/// Insert a dismissal, pruning the persisted set (T3): it never shrinks
/// otherwise and rides every launch via AppPreferences.
func trayDismissAttention(id: String, keys: Set<String>, limit: Int = 100) -> Set<String> {
  var next = keys
  if next.count >= limit, let victim = next.first(where: { $0 != id }) {
    next.remove(victim)
  }
  next.insert(id)
  return next
}

enum TrayInlineSwitchState {
  case available
  case current
  case switching
  case retry
}

/// Inline switch mark matched by route id (T4): duplicate route names across
/// providers used to light the spinner/retry on the wrong row.
func traySwitchStateForRoute(
  phase: DesignSwitchPhase,
  appID: String,
  routeID: String,
  selectedRouteID: String
) -> TrayInlineSwitchState {
  if routeID == selectedRouteID { return .current }
  switch phase {
  case let .inFlight(id, _, _, targetID) where id == appID && routeID == targetID:
    return .switching
  case let .recovery(id, _, _, routeID: wantedID) where id == appID && routeID == wantedID:
    return .retry
  default:
    return .available
  }
}

/// First unused "Work" → "Personal" → "Account N" name (T6): the fixture
/// hardcoded "Work" and duplicated on second save.
func unusedAccountName(existing: [String], candidates: [String] = ["Work", "Personal"]) -> String {
  for candidate in candidates {
    if !existing.contains(where: { $0.caseInsensitiveCompare(candidate) == .orderedSame }) {
      return candidate
    }
  }
  var suffix = existing.count + 1
  while existing.contains(where: {
    $0.caseInsensitiveCompare("Account \(suffix)") == .orderedSame
  }) {
    suffix += 1
  }
  return "Account \(suffix)"
}

// MARK: - ZCode-app plan status (ZAI captive login)

/// Warning from the ZCode desktop app's world-readable plan-status cache.
/// The app keeps its login to itself (Electron store — no plaintext
/// credential exists anywhere to import, same as other desktop apps), so
/// this only reports whether a coding plan shows connected. Non-nil means
/// "tell the user to paste a dashboard key instead".
func zcodeCodingPlanWarning(_ data: Data) -> String? {
  guard
    let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
    let entryStatus = json["entryStatus"] as? [String: Any],
    let items = entryStatus["items"] as? [String: Any],
    !items.isEmpty
  else { return nil }
  let connected = items.values.contains { item in
    guard let entry = item as? [String: Any],
      let status = entry["status"] as? String
    else { return false }
    return status != "unavailable" && !status.isEmpty
  }
  return connected
    ? nil
    : "The ZCode app reports no connected coding plan, and its login can’t be imported — paste a GLM Coding Plan key from the Z.ai dashboard (API Keys)."
}

// MARK: - Apps menu gates (AUDIT_2026-09-03 A8/A11/A12)

/// "Add API Gateway…" availability (A8). Any client can use a gateway through
/// the provider picker — the first-party match only preselects a provider in
/// the sheet, so its absence must not disable the action for
/// Kiro/Grok/OpenCode/WorkBuddy.
func canAddGateway(isBusy: Bool, gatewayProviderCount: Int) -> Bool {
  !isBusy && gatewayProviderCount > 0
}

/// True when a client has route actions but none usable and no installed
/// login: the Apps list must show an attention row, not drop the app (A11).
/// Raw action presence (not the enabled-only subset designApp renders) is
/// the signal — all-disabled actions are exactly the vanishing case.
func appNeedsAttentionPlaceholder(
  clientID: String,
  actions: [ActionSnapshot],
  providers: [AccountProviderSnapshot]
) -> Bool {
  if actions.contains(where: { $0.clientId == clientID && $0.enabled }) { return false }
  if providers.contains(where: { $0.clientId == clientID && $0.installed }) { return false }
  return actions.contains(where: { $0.clientId == clientID })
}

/// Engine "direct-account" covers two cases (A12): the client's own native
/// login and a managed route onto another provider's account (e.g. Claude on
/// a Grok account). Only the former is native — the latter must keep the
/// Models section and route management visible.
func appRouteKind(
  routeKind: String?,
  upstreamProviderID: String?,
  clientID: String
) -> DesignRouteKind {
  switch routeKind {
  case "hub": return .hub
  case "gateway": return .gateway
  case "direct-account":
    if let upstream = upstreamProviderID?.trimmingCharacters(in: .whitespacesAndNewlines),
      !upstream.isEmpty,
      nativeProviderFamily(upstream) != nativeProviderFamily(clientID)
    {
      return .account
    }
    return .native
  case "account", "pool": return .account
  default: return .account
  }
}

/// Transport for an app row. Apps with no selected route are never "Direct"
/// and never contribute to a Ready summary (P1-04).
func transportDisplayName(for app: DesignApp) -> String {
  guard let selected = app.routeOptions.first(where: { $0.id == app.selectedRouteID }) else {
    return "Not Configured"
  }
  switch selected.kind {
  case .gateway: return "Gateway"
  case .hub: return "Proxy Hub"
  case .account, .native: return "Direct"
  }
}

func isAppRouteConfigured(_ app: DesignApp) -> Bool {
  !app.selectedRouteID.isEmpty
    && app.routeOptions.contains(where: { $0.id == app.selectedRouteID })
}

func usablePublishedModelCount(_ models: [DesignPublishedModel]) -> Int {
  models.filter { !$0.needsChoice }.count
}

func activityAccessibilityLabel(for item: DesignActivity) -> String {
  let parts = [item.day, item.time, item.status.rawValue, item.title, item.detail]
    .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
    .filter { !$0.isEmpty }
  return parts.joined(separator: ", ")
}

// MARK: - Add-account sheet gates (AUDIT_2026-09-03 C2/C6/C8/C9/C10)

/// "Test Connection" enablement for AddGatewaySheet. Providers that need no
/// API key (e.g. Grok, which reads ~/.grok/auth.json) stay testable with an
/// empty key field (C2).
func gatewayConnectionTestEnabled(requiresApiKey: Bool, apiKey: String, isBusy: Bool) -> Bool {
  if isBusy { return false }
  if requiresApiKey {
    return !apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
  }
  return true
}

/// Submit gate for the saved-account path of AddSavedAccountSheet. A live
/// login without a parsed identity (e.g. WorkBuddy token with no `sub`) is
/// still saveable — save() falls back to "Current <label> login" (C8).
func addSavedAccountCanSubmit(
  name: String,
  isNewAccountMode: Bool,
  hasIdentity: Bool,
  hasLiveLogin: Bool,
  methodAvailable: Bool,
  providerEnabled: Bool,
  isBlocked: Bool
) -> Bool {
  if isBlocked { return false }
  let named = !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
  if isNewAccountMode, !hasIdentity {
    return named && methodAvailable && providerEnabled
  }
  return named && (hasIdentity || hasLiveLogin) && providerEnabled
}

/// Submit gate for the typed-credential path (API-key vendors).
func addCredentialInputCanSubmit(
  name: String,
  secret: String,
  providerEnabled: Bool,
  isBlocked: Bool
) -> Bool {
  if isBlocked { return false }
  let named = !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
  let keyed = !secret.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
  return named && keyed && providerEnabled
}

/// Inline reason rendered when Sign In / Add is disabled. The engine already
/// publishes per-method `unavailableReason`; the sheet never showed it, so a
/// missing launcher read as a dead button (C6). Nil means "no blocker".
func addAccountBlockerMessage(
  provider: DesignProviderOption?,
  method: DesignLoginMethod?,
  isNewAccountMode: Bool
) -> String? {
  guard let provider else {
    return "No compatible providers are installed. Sign in with a supported app, then try again."
  }
  if !provider.enabled {
    return provider.detail.isEmpty
      ? "\(provider.label) is not available on this Mac." : provider.detail
  }
  if isNewAccountMode, let method, !method.available {
    if let reason = method.unavailableReason, !reason.isEmpty { return reason }
    return method.detail.isEmpty ? "\(method.label) is not available." : method.detail
  }
  return nil
}

/// Parse "Detected <identity> in <source>" from account-detect results.
/// Matches the LAST " in " so identities containing " in " survive (C9).
func accountIdentity(fromDetectionMessage message: String?) -> String? {
  guard let message, message.hasPrefix("Detected ") else { return nil }
  let body = message.dropFirst("Detected ".count)
  guard let range = body.range(of: " in ", options: .backwards) else { return nil }
  let identity = String(body[..<range.lowerBound])
  if identity.isEmpty { return nil }
  return identity == "the current login" ? "Current login" : identity
}
