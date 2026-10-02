package proxy

import (
	"context"
	"net/url"
	"reflect"
	"testing"
)

// upstreamCarry declares, for every field of Upstream, whether it must travel
// each hop of the proxy's egress plumbing:
//
//   - server: buildServer copies it from the resolved Upstream onto the Server.
//   - value:  upstreamValue copies it back for Serve.
//   - flat:   flattenedNativeUpstream keeps it on the native-Anthropic merge.
//
// A field with no row fails the build: adding an egress fact without saying
// where it travels is how SanitizeAnthropicBody shipped in the struct while
// native forwards silently skipped it. Intentional drops name their reason:
// CatalogScope is minted per account by buildServer, never inherited, and
// QuotaErrorPatterns is read by the pool ring straight from the resolved
// Upstream (pool_server.go) — single-account servers never fail over, so
// carrying it there would be a dead copy next to the live one.
var upstreamCarry = map[string]struct {
	server string // Server field name; "" = intentionally not carried
	value  bool
	flat   bool
}{
	"BaseURL":               {server: "Upstream", value: true, flat: true},
	"Auth":                  {server: "UpstreamAuth", value: true, flat: true},
	"AuthHeader":            {server: "UpstreamAuthHeader", value: true, flat: true},
	"AuthValuePrefix":       {server: "UpstreamAuthValuePrefix", value: true, flat: true},
	"AuthProvider":          {server: "UpstreamAuthProvider", value: true, flat: true},
	"ExtraHeaders":          {server: "UpstreamExtraHeaders", value: true, flat: true},
	"DynamicHeaders":        {server: "UpstreamDynamicHeaders", value: true, flat: true},
	"ModelDiscovery":        {server: "UpstreamModelDiscovery", value: true, flat: false},
	"CatalogScope":          {server: "", value: true, flat: false},
	"Anthropic":             {server: "Anthropic", value: true, flat: false},
	"TranslateAnthropic":    {server: "TranslateAnthropic", value: true, flat: false},
	"TranslateGemini":       {server: "TranslateGemini", value: true, flat: false},
	"Gemini":                {server: "Gemini", value: true, flat: false},
	"OpenAIPath":            {server: "OpenAIPath", value: true, flat: false},
	"SanitizeOpenAIBody":    {server: "SanitizeOpenAIBody", value: true, flat: false},
	"SanitizeAnthropicBody": {server: "SanitizeAnthropicBody", value: true, flat: true},
	"QuotaErrorPatterns":    {server: "", value: false, flat: false},
	"MapModel":              {server: "MapModel", value: true, flat: false},
}

func funcPointer(f any) uintptr {
	if f == nil {
		return 0
	}
	return reflect.ValueOf(f).Pointer()
}

func markerUpstream(t *testing.T) *Upstream {
	t.Helper()
	base, _ := url.Parse("https://upstream.example/v1")
	return &Upstream{
		BaseURL:               base,
		Auth:                  "auth-marker",
		AuthHeader:            "x-marker",
		AuthValuePrefix:       "Marker ",
		AuthProvider:          func(context.Context) (string, error) { return "live", nil },
		ExtraHeaders:          []ExtraHeader{{Name: "x-m", Value: "1"}},
		DynamicHeaders:        func(context.Context) ([]ExtraHeader, error) { return nil, nil },
		ModelDiscovery:        func(context.Context) ([]string, error) { return []string{"m"}, nil },
		CatalogScope:          "scope-marker",
		Anthropic:             &UpstreamTarget{BaseURL: base},
		TranslateAnthropic:    true,
		TranslateGemini:       true,
		Gemini:                &GeminiTarget{},
		OpenAIPath:            "/chat",
		SanitizeOpenAIBody:    func([]byte) []byte { return nil },
		SanitizeAnthropicBody: func([]byte) []byte { return nil },
		QuotaErrorPatterns:    []string{"marker"},
		MapModel:              func(s string) string { return s },
	}
}

func upstreamField(up *Upstream, name string) reflect.Value {
	return reflect.ValueOf(up).Elem().FieldByName(name)
}

func serverField(srv *Server, name string) reflect.Value {
	return reflect.ValueOf(srv).Elem().FieldByName(name)
}

// Every Upstream field needs an explicit carry row: an unclassified field is
// a wiring decision nobody made, and the test fails until it is classified.
func TestUpstreamCarryCoversEveryField(t *testing.T) {
	typ := reflect.TypeOf(Upstream{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		row, ok := upstreamCarry[name]
		if !ok {
			t.Errorf("Upstream.%s has no carry expectation: classify it in upstreamCarry (server/value/flat)", name)
			continue
		}
		if row.server != "" {
			if _, ok := reflect.TypeOf(Server{}).FieldByName(row.server); !ok {
				t.Errorf("Upstream.%s maps to missing Server.%s", name, row.server)
			}
		}
	}
}

// buildServer must copy every carried field onto the Server (except
// CatalogScope, which it mints from its arguments).
func TestBuildServerCarriesUpstreamFields(t *testing.T) {
	up := markerUpstream(t)
	srv := (&Service{}).buildServer("prov", "acc", mustNewInstanceID(t), "tok", up)
	for name, row := range upstreamCarry {
		if row.server == "" {
			continue
		}
		got, want := serverField(srv, row.server), upstreamField(up, name)
		switch want.Kind() {
		case reflect.Func, reflect.Ptr:
			if got.Pointer() != want.Pointer() {
				t.Errorf("Server.%s lost Upstream.%s", row.server, name)
			}
		default:
			if !reflect.DeepEqual(got.Interface(), want.Interface()) {
				t.Errorf("Server.%s = %#v, want Upstream.%s = %#v", row.server, got.Interface(), name, want.Interface())
			}
		}
	}
	if srv.CatalogScope != "account/prov/acc" {
		t.Errorf("CatalogScope = %q, want the account-minted scope (never inherited)", srv.CatalogScope)
	}
}

// upstreamValue must hand every Server mirror back to Serve.
func TestUpstreamValueCarriesServerFields(t *testing.T) {
	up := markerUpstream(t)
	srv := (&Service{}).buildServer("prov", "acc", mustNewInstanceID(t), "tok", up)
	back := srv.upstreamValue()
	for name, row := range upstreamCarry {
		if !row.value || name == "CatalogScope" {
			continue
		}
		got, want := upstreamField(back, name), upstreamField(up, name)
		switch want.Kind() {
		case reflect.Func, reflect.Ptr:
			if got.Pointer() != want.Pointer() {
				t.Errorf("upstreamValue lost %s", name)
			}
		default:
			if !reflect.DeepEqual(got.Interface(), want.Interface()) {
				t.Errorf("upstreamValue.%s = %#v, want %#v", name, got.Interface(), want.Interface())
			}
		}
	}
}

// flattenedNativeUpstream merges parent + target for exactly one leaf (the
// native forward): target wins auth presentation, the parent fills the rest.
// Provider-owned body rewrites ride the parent — dropping one here silently
// re-enables the vendor rejection it exists to suppress.
func TestFlattenedUpstreamKeepsDeclaredFields(t *testing.T) {
	up := markerUpstream(t)
	targetBase, _ := url.Parse("https://vendor.example/anthropic")
	up.Anthropic = &UpstreamTarget{
		BaseURL:         targetBase,
		AuthHeader:      "x-api-key",
		AuthValuePrefix: "",
		AuthProvider:    func(context.Context) (string, error) { return "target-live", nil },
		ExtraHeaders:    []ExtraHeader{{Name: "x-t", Value: "1"}},
	}
	flat := flattenedNativeUpstream(up)
	if flat.BaseURL.String() != targetBase.String() {
		t.Errorf("flat base = %s, want the target base", flat.BaseURL)
	}
	if flat.AuthProvider == nil || funcPointer(flat.AuthProvider) != funcPointer(up.Anthropic.AuthProvider) {
		t.Error("flat auth must come from the target when it sets one")
	}
	if len(flat.ExtraHeaders) != 2 {
		t.Fatalf("flat headers = %v, want target + parent", flat.ExtraHeaders)
	}
	if funcPointer(flat.DynamicHeaders) != funcPointer(up.DynamicHeaders) {
		t.Error("flat must keep the parent DynamicHeaders")
	}
	if funcPointer(flat.SanitizeAnthropicBody) != funcPointer(up.SanitizeAnthropicBody) {
		t.Error("flat must keep the parent SanitizeAnthropicBody")
	}
	if flat.ModelDiscovery != nil || flat.TranslateAnthropic || flat.TranslateGemini ||
		flat.Gemini != nil || flat.OpenAIPath != "" || flat.SanitizeOpenAIBody != nil ||
		len(flat.QuotaErrorPatterns) != 0 || flat.Anthropic != nil {
		t.Error("flat must not carry discovery/translation/catalog facts: the native leaf forwards verbatim")
	}
	// Empty target inherits the parent presentation.
	bare := markerUpstream(t)
	bareBase, _ := url.Parse("https://vendor.example/plain")
	bare.Anthropic = &UpstreamTarget{BaseURL: bareBase}
	flatBare := flattenedNativeUpstream(bare)
	if flatBare.AuthHeader != bare.AuthHeader || flatBare.AuthValuePrefix != bare.AuthValuePrefix {
		t.Error("flat must inherit parent auth presentation when the target sets none")
	}
	if funcPointer(flatBare.AuthProvider) != funcPointer(bare.AuthProvider) {
		t.Error("flat must inherit the parent AuthProvider when the target sets none")
	}
}
