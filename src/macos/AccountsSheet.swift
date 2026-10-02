import SwiftUI

enum AccountsSheet: String, Identifiable {
  case addAccount
  case addGateway

  var id: String { rawValue }
}
