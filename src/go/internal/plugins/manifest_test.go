package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestParseManifestValidatesTrustBoundaryFields(t *testing.T) {
	valid := `{
		"name":"acme-plugin","version":"1.2.3","description":"ACME",
		"apiVersion":1,"main":"dist/index.mjs","ignored":true
	}`
	manifest, err := ParseManifest([]byte(valid), "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "acme-plugin" || manifest.Version != "1.2.3" ||
		manifest.Description != "ACME" || manifest.Main != "dist/index.mjs" ||
		manifest.APIVersion != APIVersion {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"invalid JSON", `{`, "not valid JSON"},
		{"scalar", `true`, "JSON object"},
		{"array", `[]`, `invalid "name"`},
		{"uppercase name", `{"name":"ACME","version":"1","apiVersion":1,"main":"x"}`, `invalid "name"`},
		{"one-char name", `{"name":"a","version":"1","apiVersion":1,"main":"x"}`, `invalid "name"`},
		{"missing version", `{"name":"acme","apiVersion":1,"main":"x"}`, `missing a "version"`},
		{"missing main", `{"name":"acme","version":"1","apiVersion":1}`, `missing a "main"`},
		{"wrong API", `{"name":"acme","version":"1","apiVersion":999,"main":"x"}`, "plugin API 999"},
		{"missing API", `{"name":"acme","version":"1","main":"x"}`, "plugin API undefined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.raw), "manifest")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			var structured *anyerr.Error
			if !errors.As(err, &structured) || structured.Code != "PLUGIN_INVALID" {
				t.Fatalf("error is not PLUGIN_INVALID: %#v", err)
			}
		})
	}
}

func TestParseManifestFormatsAPIVersionLikeJavaScriptString(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"name":"acme","version":"1","apiVersion":null,"main":"x"}`, "plugin API null"},
		{`{"name":"acme","version":"1","apiVersion":{},"main":"x"}`, "plugin API [object Object]"},
		{`{"name":"acme","version":"1","apiVersion":[2,null,3],"main":"x"}`, "plugin API 2,,3"},
		{`{"name":"acme","version":"1","apiVersion":0.000001,"main":"x"}`, "plugin API 0.000001"},
		{`{"name":"acme","version":"1","apiVersion":1e-7,"main":"x"}`, "plugin API 1e-7"},
		{`{"name":"acme","version":"1","apiVersion":1e21,"main":"x"}`, "plugin API 1e+21"},
	}
	for _, tc := range cases {
		_, err := ParseManifest([]byte(tc.raw), "manifest")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseManifest(%s) error = %q, want %q", tc.raw, err, tc.want)
		}
	}
}

func TestReadManifestAndResolveEntry(t *testing.T) {
	root := t.TempDir()
	_, err := ReadManifest(root)
	if err == nil || !strings.Contains(err.Error(), "No "+ManifestFile) {
		t.Fatalf("missing manifest error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, ManifestFile), []byte(
		`{"name":"acme-plugin","version":"1","apiVersion":1,"main":"dist/index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(root)
	if err != nil || manifest.Name != "acme-plugin" {
		t.Fatalf("manifest = %+v err=%v", manifest, err)
	}
	entry, err := ResolveEntry(root, manifest.Main)
	if err != nil || entry != filepath.Join(root, "dist", "index.mjs") {
		t.Fatalf("entry = %q err=%v", entry, err)
	}
	for _, main := range []string{"../../outside.mjs", filepath.Join(string(filepath.Separator), "outside.mjs")} {
		if _, err := ResolveEntry(root, main); err == nil {
			t.Fatalf("ResolveEntry accepted %q", main)
		}
	}
}
