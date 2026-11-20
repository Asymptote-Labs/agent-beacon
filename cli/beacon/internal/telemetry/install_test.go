package telemetry

import (
	"testing"
	"time"
)

func TestRecordInstalledKeepsTheFirstInstall(t *testing.T) {
	_, home := setup(t, nil)
	Init(options(nil, nil))
	if InstalledBefore() {
		t.Fatal("a fresh home reports an earlier install")
	}
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	RecordInstalled(first)
	RecordInstalled(first.Add(time.Hour))
	if !InstalledBefore() {
		t.Fatal("an install was not remembered")
	}
	if got := loadState(home).InstalledAt; got != "2026-11-01T09:00:00Z" {
		t.Fatalf("installed_at = %q, want the first install", got)
	}

	// A later run (a reinstall after uninstall) still knows.
	Close()
	Init(options(nil, nil))
	if !InstalledBefore() {
		t.Fatal("the install was forgotten by the next run")
	}
}

func TestInstalledBeforeIsFalseWithTelemetryOff(t *testing.T) {
	setup(t, nil)
	Init(options(map[string]string{EnvTelemetry: "0"}, nil))
	RecordInstalled(time.Now())
	if InstalledBefore() {
		t.Fatal("telemetry off reported an install")
	}
}
