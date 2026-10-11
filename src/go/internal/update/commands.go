package update

import "fmt"

// PackageManagerCommand returns the shell command (or install instruction)
// shown for a package-manager channel. deb/rpm never mention a "repo" — they
// are manual reinstalls of a downloaded local file.
func PackageManagerCommand(ch InstallChannel, version string) string {
	ver := version
	if ver == "" {
		ver = "<version>"
	}
	switch ch {
	case ChannelBrew:
		return "brew upgrade --cask anypick"
	case ChannelScoop:
		return "scoop update anypick"
	case ChannelNPM:
		return "npm install -g anypick@latest"
	case ChannelDeb:
		return fmt.Sprintf("sudo apt install ./AnyPick_%s_amd64.deb", ver)
	case ChannelRPM:
		return fmt.Sprintf("sudo dnf install ./AnyPick-%s.x86_64.rpm", ver)
	case ChannelWinget:
		return "winget upgrade AnyPick"
	case ChannelMSI:
		return fmt.Sprintf("Download AnyPick_%s_x64_en-US.msi and reinstall", ver)
	default:
		return ""
	}
}

// SkipVersionCopy is the UI string shown after the user skips a version.
func SkipVersionCopy(version string) string {
	return fmt.Sprintf("You skipped %s. You'll hear about the next version.", version)
}

// ReadyAuthenticityLine is the Ready-state line. Avoid the bare word "verified"
// until minisign lands.
const ReadyAuthenticityLine = "Downloaded from GitHub Releases. The file matches the release checksum."

// MacOSReadyFootnote is shown under Ready on macOS direct installs.
const MacOSReadyFootnote = "macOS may ask again for access AnyPick already had, such as Keychain or Automation, because this build isn't notarized. Allow it to keep things working."

// RelaunchProxiesConfirm formats the quit/relaunch confirmation when proxies
// are serving.
func RelaunchProxiesConfirm(n int) string {
	return fmt.Sprintf("Restarting stops %d running proxies. Connected apps reconnect when AnyPick starts again.", n)
}

// RestartMenuLabel is the menu bar item when an update is ready.
func RestartMenuLabel(version string) string {
	return fmt.Sprintf("Restart to Update to %s", version)
}
