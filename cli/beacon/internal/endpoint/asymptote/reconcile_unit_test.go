package asymptote

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

// Keep service operations fake, while checking and writing the actual production
// launchd plist in an isolated HOME. The legacy-marker fallback made the initial
// regression runnable before the production LogPathsCurrent method existed.
type unitReconcileForwarder struct {
	*fakeForwarder
	manager    service.ForwarderManager
	unitWrites int
}

func (f *unitReconcileForwarder) LogPathsCurrent() (bool, error) {
	if checker, ok := any(f.manager).(interface{ LogPathsCurrent() (bool, error) }); ok {
		return checker.LogPathsCurrent()
	}
	data, err := os.ReadFile(f.unitPath)
	return !bytes.Contains(data, []byte("/tmp/com.beacon.endpoint.asymptote-forwarder")), err
}
func (f *unitReconcileForwarder) WriteUnit(vector, config string) (string, error) {
	f.unitWrites++
	if f.writeErr != nil {
		return "", f.writeErr
	}
	return f.manager.WriteUnit(vector, config)
}

func legacyUnitFixture(t *testing.T) (*reconcileFixture, *unitReconcileForwarder) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("launchd unit writes require macOS")
	}
	f := newReconcileFixture(t, "standard")
	manager := service.ForwarderManager{UserMode: true, Kind: service.KindLaunchd}
	path, err := manager.UnitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `<?xml version="1.0"?><plist version="1.0"><dict>
 <key>StandardOutPath</key><string>/tmp/com.beacon.endpoint.asymptote-forwarder.out</string>
 <key>StandardErrorPath</key><string>/tmp/com.beacon.endpoint.asymptote-forwarder.err</string>
 </dict></plist>`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	f.fwd.unitPath = path
	return f, &unitReconcileForwarder{fakeForwarder: f.fwd, manager: manager}
}

func TestReconcileMigratesLegacyUnitEvenWhenConfigCurrent(t *testing.T) {
	f, manager := legacyUnitFixture(t)
	before, _ := os.ReadFile(VectorConfigPath(true))
	beforeInfo, _ := os.Stat(VectorConfigPath(true))
	loads := f.fwd.loads
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged || !result.Restarted || result.Warning != "" || manager.unitWrites != 1 || f.fwd.loads != loads+1 {
		t.Fatalf("result=%+v unitWrites=%d loads=%d->%d; want one unit rewrite and one restart", result, manager.unitWrites, loads, f.fwd.loads)
	}
	after, _ := os.ReadFile(VectorConfigPath(true))
	afterInfo, _ := os.Stat(VectorConfigPath(true))
	if !bytes.Equal(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("unit-only migration rewrote config")
	}
	unit, _ := os.ReadFile(f.fwd.unitPath)
	if bytes.Contains(unit, []byte("/tmp/com.beacon.endpoint.asymptote-forwarder")) || !bytes.Contains(unit, []byte("Library/Logs/Beacon/")) {
		t.Fatalf("unit was not migrated: %s", unit)
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
	result, err = ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
	if err != nil || !result.Unchanged || manager.unitWrites != 1 || f.fwd.loads != loads+1 {
		t.Fatalf("repeat result=%+v err=%v writes=%d loads=%d", result, err, manager.unitWrites, f.fwd.loads)
	}
}

func TestReconcileUnitMigrationKeepsStoppedServicesStopped(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		t.Run(fmtBool(loaded), func(t *testing.T) {
			f, manager := legacyUnitFixture(t)
			f.fwd.stopped = true
			if !loaded {
				f.fwd.loads = 0
			}
			loads := f.fwd.loads
			result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
			if err != nil || !result.UnitUpdated || result.Updated || result.Restarted || result.Warning != "" || manager.unitWrites != 1 || f.fwd.loads != loads {
				t.Fatalf("result=%+v err=%v writes=%d loads=%d->%d", result, err, manager.unitWrites, loads, f.fwd.loads)
			}
			f.assertCredentialsAndCheckpointsUntouched(t)
		})
	}
}
func fmtBool(loaded bool) string {
	if loaded {
		return "loaded"
	}
	return "unloaded"
}

func TestReconcileUnitMigrationReportsWriteFailure(t *testing.T) {
	f, manager := legacyUnitFixture(t)
	before, _ := os.ReadFile(f.fwd.unitPath)
	config, _ := os.ReadFile(VectorConfigPath(true))
	loads := f.fwd.loads
	f.fwd.writeErr = errors.New("unit write refused")
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
	if err != nil || result.UnitUpdated || result.Updated || result.Restarted || !strings.Contains(result.Warning, "unit write refused") || f.fwd.loads != loads {
		t.Fatalf("result=%+v err=%v loads=%d->%d", result, err, loads, f.fwd.loads)
	}
	after, _ := os.ReadFile(f.fwd.unitPath)
	afterConfig, _ := os.ReadFile(VectorConfigPath(true))
	if !bytes.Equal(before, after) || !bytes.Equal(config, afterConfig) {
		t.Fatal("failed unit migration changed a file")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}

func TestReconcileUnitMigrationReportsReloadFailure(t *testing.T) {
	f, manager := legacyUnitFixture(t)
	f.fwd.loadErr = errors.New("reload refused")
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
	if err != nil || !result.UnitUpdated || result.Restarted || !strings.Contains(result.Warning, "reload refused") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}

func TestReconcileUnitMigrationPreservesCurrentOperatorSettings(t *testing.T) {
	f, manager := legacyUnitFixture(t)
	if _, err := manager.manager.WriteUnit(f.vector, VectorConfigPath(true)); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(f.fwd.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.ReplaceAll(strings.Replace(string(unit), f.vector, "/operator/custom/vector", 1), "  ", "    ")
	if err := os.WriteFile(f.fwd.unitPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	loads := f.fwd.loads
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
	if err != nil || !result.Unchanged || result.UnitUpdated || result.Restarted || manager.unitWrites != 0 || f.fwd.loads != loads {
		t.Fatalf("result=%+v err=%v writes=%d loads=%d", result, err, manager.unitWrites, f.fwd.loads)
	}
	after, _ := os.ReadFile(f.fwd.unitPath)
	if string(after) != custom {
		t.Fatal("current unit operator settings were rewritten")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}

func TestReconcileUnitMigrationRefusesMalformedUnit(t *testing.T) {
	for _, kind := range []string{"incomplete XML", "leading text"} {
		t.Run(kind, func(t *testing.T) {
			f, manager := legacyUnitFixture(t)
			broken := []byte("<plist><dict>")
			if kind == "leading text" {
				legacy, err := os.ReadFile(f.fwd.unitPath)
				if err != nil {
					t.Fatal(err)
				}
				broken = append([]byte("junk"), legacy...)
			}
			if err := os.WriteFile(f.fwd.unitPath, broken, 0o644); err != nil {
				t.Fatal(err)
			}
			loads := f.fwd.loads
			config, err := os.ReadFile(VectorConfigPath(true))
			if err != nil {
				t.Fatal(err)
			}
			result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
			if err != nil || result.UnitUpdated || result.Updated || result.Restarted || result.Warning == "" || manager.unitWrites != 0 || f.fwd.loads != loads {
				t.Fatalf("result=%+v err=%v writes=%d loads=%d", result, err, manager.unitWrites, f.fwd.loads)
			}
			after, _ := os.ReadFile(f.fwd.unitPath)
			afterConfig, _ := os.ReadFile(VectorConfigPath(true))
			if !bytes.Equal(after, broken) || !bytes.Equal(afterConfig, config) {
				t.Fatal("malformed unit or config overwritten")
			}
			f.assertCredentialsAndCheckpointsUntouched(t)
		})
	}
}

func TestReconcileUnitMigrationReportsConfigAlreadyWrittenBeforeUnitFailure(t *testing.T) {
	f, manager := legacyUnitFixture(t)
	configPath := VectorConfigPath(true)
	current, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(current, []byte("\n# older pack\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(f.fwd.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	loads := f.fwd.loads
	f.fwd.writeErr = errors.New("unit write refused")
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: manager})
	if err != nil || !result.Updated || result.UnitUpdated || result.Restarted || !strings.Contains(result.Warning, "unit write refused") || !strings.Contains(result.Warning, "config updated") || f.fwd.loads != loads {
		t.Fatalf("result=%+v err=%v loads=%d->%d", result, err, loads, f.fwd.loads)
	}
	after, _ := os.ReadFile(configPath)
	afterUnit, _ := os.ReadFile(f.fwd.unitPath)
	if !bytes.Equal(after, current) || !bytes.Equal(afterUnit, unit) {
		t.Fatal("partial-update result does not match files")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}
