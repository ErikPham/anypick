import AppKit
import SwiftUI

enum TrayMotion {
  static var standard: Animation {
    reduceMotionEnabled ? .easeOut(duration: 0.01) : .easeInOut(duration: 0.18)
  }

  static var message: Animation {
    reduceMotionEnabled ? .easeOut(duration: 0.01) : .spring(response: 0.32, dampingFraction: 0.86)
  }
}

func trayHapticSuccess() {
  NSHapticFeedbackManager.defaultPerformer.perform(.generic, performanceTime: .default)
}

func trayHapticError() {
  NSHapticFeedbackManager.defaultPerformer.perform(.alignment, performanceTime: .default)
}

private var reduceMotionEnabled: Bool {
  NSWorkspace.shared.accessibilityDisplayShouldReduceMotion
}
