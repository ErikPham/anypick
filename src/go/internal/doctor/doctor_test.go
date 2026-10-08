package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/store"
)

func TestDiagnoseInstallationMissingDatabaseStaysUnhealthy(t *testing.T) {
	report := DiagnoseInstallation(context.Background(), InstallationDeps{
		Root: t.TempDir(),
		Environment: EnvironmentReport{Proxy: ProxyEnvironment{
			Warnings: []string{"HTTP_PROXY is set"},
		}},
	})
	if report.Status != StatusUnhealthy {
		t.Fatalf("status = %q, want unhealthy when the database is missing", report.Status)
	}
	sawDB, sawEnv := false, false
	for _, finding := range report.Findings {
		if finding.Check == "database" && finding.Status == "fail" {
			sawDB = true
		}
		if finding.Check == "proxy-environment" && finding.Status == "warn" {
			sawEnv = true
		}
	}
	if !sawDB || !sawEnv {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestDiagnoseInstallationEmptyDatabaseCanBeOK(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "anypick.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	report := DiagnoseInstallation(context.Background(), InstallationDeps{Root: root})
	if report.Status != StatusOK {
		t.Fatalf("status = %q findings=%+v", report.Status, report.Findings)
	}
}

// A named profile whose account is gone is a credential with no owner, and the
// doctor is the only read-only surface that can say so without starting the
// cleanup. The finding must name the profile and the missing account.
func TestDiagnoseInstallationReportsOrphanedNamedProfile(t *testing.T) {
	root, svc := doctorTestServices(t)
	report := DiagnoseInstallation(context.Background(), InstallationDeps{
		Root:     root,
		Accounts: svc,
		NamedProfiles: func(context.Context) ([]NamedProfileRef, error) {
			return []NamedProfileRef{
				{Client: "codex", Name: "gone", SourceKind: "account",
					Provider: "codex", AccountName: "deleted"},
				{Client: "codex", Name: "gateway", SourceKind: "gateway"},
			}, nil
		},
	})
	if report.Status != StatusDegraded {
		t.Fatalf("status = %q, want degraded for an orphaned profile", report.Status)
	}
	var orphan, gateway int
	for _, f := range report.Findings {
		if f.Check != "named-profile" {
			continue
		}
		if strings.Contains(f.Detail, "codex/gone") {
			orphan++
		}
		if strings.Contains(f.Detail, "gateway") {
			gateway++
		}
	}
	if orphan != 1 {
		t.Fatalf("orphaned profile finding = %d, want exactly one (%+v)", orphan, report.Findings)
	}
	if gateway != 0 {
		t.Fatalf("a gateway-backed profile was reported as orphaned: %+v", report.Findings)
	}
}

// A profile audit that cannot read the store must not read as "all clean".
func TestDiagnoseInstallationReportsProfileAuditFailure(t *testing.T) {
	root, svc := doctorTestServices(t)
	report := DiagnoseInstallation(context.Background(), InstallationDeps{
		Root:     root,
		Accounts: svc,
		NamedProfiles: func(context.Context) ([]NamedProfileRef, error) {
			return nil, errors.New("database is locked")
		},
	})
	found := false
	for _, f := range report.Findings {
		if f.Check == "named-profile" && strings.Contains(f.Detail, "database is locked") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a failed profile audit produced no finding: %+v", report.Findings)
	}
}

// doctorTestServices opens a real (empty) database under a temp root and hands
// back the account service the profile audit reads through. An empty file named
// anypick.db is not a database: Accounts.Get against it fails, which would make
// the audit report a lookup failure instead of the orphan under test.
func doctorTestServices(t *testing.T) (string, *accounts.Service) {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	reg := provider.NewRegistry()
	if err := provider.RegisterBuiltin(reg, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return root, accounts.New(store.New(root, database), reg)
}
