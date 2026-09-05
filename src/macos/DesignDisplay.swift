import Foundation

// Shared display helpers for macOS panes and Quick Panel (P1-01…P1-04).
// Centralizes route canonicalization, identity formatting, and friendly
// copy so planner and executor cannot diverge on ad-hoc strings.

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
