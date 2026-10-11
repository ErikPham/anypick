package update

import "testing"

func TestValidateDownloadURLAllowlist(t *testing.T) {
	ok := []string{
		"https://github.com/outof0/anypick/releases/download/v1.1.0/x.zip",
		"https://objects.githubusercontent.com/github-production-release-asset/123/x",
	}
	for _, u := range ok {
		if err := ValidateDownloadURL(u); err != nil {
			t.Fatalf("allow %q: %v", u, err)
		}
	}
	bad := []string{
		"http://github.com/outof0/anypick/x",
		"https://evil.com/x",
		"https://release-assets.githubusercontent.com/x",
		"https://api.github.com/repos/outof0/anypick/x",
		"https://github.com.evil.com/x",
		"ftp://github.com/x",
		"",
	}
	for _, u := range bad {
		if err := ValidateDownloadURL(u); err == nil {
			t.Fatalf("expected reject for %q", u)
		}
	}
}
