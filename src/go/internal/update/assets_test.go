package update

import "testing"

func v110Assets() Release {
	names := []string{
		"AnyPick-macos-universal.zip",
		"AnyPick_1.1.0_x64-setup.exe",
		"AnyPick_1.1.0_x64_en-US.msi",
		"AnyPick-1.1.0-windows-x64.msi",
		"AnyPick_1.1.0_amd64.AppImage",
		"AnyPick_1.1.0_amd64.deb",
		"AnyPick-1.1.0-1.x86_64.rpm",
		"anypick-1.1.0-darwin-arm64.tar.gz",
		"anypick-1.1.0-linux-amd64.tar.gz",
		"anypick-1.1.0-linux-arm64.tar.gz",
		"anypick-1.1.0-windows-amd64.zip",
		"SHA256SUMS.txt",
	}
	var assets []Asset
	for _, n := range names {
		assets = append(assets, Asset{Name: n, URL: "https://example/" + n})
	}
	return Release{Tag: "v1.1.0", Version: "1.1.0", Assets: assets}
}

func TestPickAssetV110Names(t *testing.T) {
	rel := v110Assets()
	cases := []struct {
		goos, goarch string
		ch           InstallChannel
		want         string
	}{
		{"darwin", "arm64", ChannelDirect, "AnyPick-macos-universal.zip"},
		{"darwin", "amd64", ChannelBrew, "AnyPick-macos-universal.zip"},
		{"windows", "amd64", ChannelNSIS, "AnyPick_1.1.0_x64-setup.exe"},
		{"windows", "amd64", ChannelMSI, "AnyPick_1.1.0_x64_en-US.msi"},
		{"windows", "amd64", ChannelWinget, "AnyPick-1.1.0-windows-x64.msi"},
		{"linux", "amd64", ChannelAppImage, "AnyPick_1.1.0_amd64.AppImage"},
		{"linux", "amd64", ChannelDeb, "AnyPick_1.1.0_amd64.deb"},
		{"linux", "amd64", ChannelRPM, "AnyPick-1.1.0-1.x86_64.rpm"},
		{"linux", "amd64", ChannelNPM, "anypick-1.1.0-linux-amd64.tar.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			a, err := PickAsset(rel, tc.goos, tc.goarch, tc.ch)
			if err != nil {
				t.Fatal(err)
			}
			if a.Name != tc.want {
				t.Fatalf("got %q", a.Name)
			}
		})
	}
}

func TestPickAssetArm64DesktopFallback(t *testing.T) {
	rel := v110Assets()
	_, err := PickAsset(rel, "linux", "arm64", ChannelDirect)
	if err == nil {
		t.Fatal("expected manual-download error for linux/arm64 desktop")
	}
	_, err = PickAsset(rel, "windows", "arm64", ChannelNSIS)
	if err == nil {
		t.Fatal("expected error for windows/arm64 NSIS")
	}
}

func TestPackageManagerCommands(t *testing.T) {
	if got := PackageManagerCommand(ChannelDeb, "1.2.0"); got != "sudo apt install ./AnyPick_1.2.0_amd64.deb" {
		t.Fatalf("deb: %q", got)
	}
	if got := PackageManagerCommand(ChannelRPM, "1.2.0"); got != "sudo dnf install ./AnyPick-1.2.0.x86_64.rpm" {
		t.Fatalf("rpm: %q", got)
	}
	if got := PackageManagerCommand(ChannelWinget, "1.2.0"); got != "winget upgrade AnyPick" {
		t.Fatalf("winget: %q", got)
	}
	for _, ch := range []InstallChannel{ChannelDeb, ChannelRPM} {
		cmd := PackageManagerCommand(ch, "1.0.0")
		if containsFold(cmd, "repo") {
			t.Fatalf("%s command must not say repo: %q", ch, cmd)
		}
	}
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (stringFind(s, sub) >= 0)))
}

func stringFind(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
