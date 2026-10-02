package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
