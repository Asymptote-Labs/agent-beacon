package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	endpointcollector "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

// repairServiceManager is the subset of the service manager the collector repair flow
// needs. It is an interface so tests can substitute a fake manager.
type repairServiceManager interface {
	PlistPath() (string, error)
	WritePlist(program, configPath string) (string, error)
	Load() error
	Unload() error
}

type repairFileSnapshot struct {
	existed bool
	data    []byte
	mode    os.FileMode
	readErr error
}

type repairRollback struct {
	manager          repairServiceManager
	serviceWasLoaded bool
	serviceLoaded    bool
	files            []string
	snapshots        map[string]repairFileSnapshot
}

// These package-level seams let tests stub the collector repair dependencies.
var (
	repairLoadEndpointConfig     = endpointconfig.Load
	repairSaveEndpointConfig     = endpointconfig.Save
	repairResolveCollectorBinary = endpointcollector.ResolveBinary
	repairWriteCollectorConfig   = endpointcollector.WriteConfig
	repairWaitCollectorReady     = endpointcollector.WaitUntilReady
	newRepairServiceManager      = func(userMode bool) repairServiceManager {
		return service.Manager{UserMode: userMode}
	}
)

func repairCollectorServiceFromStatus(status lifecycle.Status) error {
	userMode := status.RuntimeLog.EffectiveUserMode
	cfg, err := repairLoadEndpointConfig(userMode)
	if err != nil {
		return err
	}
	manager := newRepairServiceManager(userMode)
	plistPath, err := manager.PlistPath()
	if err != nil {
		return err
	}
	rollback := newRepairRollback(manager, status.Service.Loaded)
	rollback.Track(endpointconfig.ConfigPath(userMode))
	rollback.Track(cfg.Collector.ConfigPath)
	rollback.Track(plistPath)

	if endpointOpts.logPath != "" {
		cfg.LogPath = endpointOpts.logPath
		if _, err := repairSaveEndpointConfig(cfg); err != nil {
			return rollbackRepairError(err, rollback)
		}
	}
	binary, err := repairResolveCollectorBinary(cfg.Collector.BinaryPath)
	if err != nil {
		return rollbackRepairError(err, rollback)
	}
	if err := repairWriteCollectorConfig(cfg); err != nil {
		return rollbackRepairError(err, rollback)
	}
	if _, err := manager.WritePlist(binary, cfg.Collector.ConfigPath); err != nil {
		return rollbackRepairError(err, rollback)
	}
	if err := manager.Load(); err != nil {
		return rollbackRepairError(err, rollback)
	}
	rollback.serviceLoaded = true
	if err := repairWaitCollectorReady(cfg, 10*time.Second); err != nil {
		return rollbackRepairError(err, rollback)
	}
	return nil
}

func newRepairRollback(manager repairServiceManager, serviceWasLoaded bool) *repairRollback {
	return &repairRollback{
		manager:          manager,
		serviceWasLoaded: serviceWasLoaded,
		snapshots:        map[string]repairFileSnapshot{},
	}
}

func (r *repairRollback) Track(path string) {
	if r == nil || path == "" {
		return
	}
	if _, ok := r.snapshots[path]; ok {
		return
	}
	r.snapshots[path] = snapshotRepairFile(path)
	r.files = append(r.files, path)
}

func (r *repairRollback) Rollback() error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.serviceLoaded {
		if err := r.manager.Unload(); err != nil {
			errs = append(errs, err)
		}
	}
	for i := len(r.files) - 1; i >= 0; i-- {
		path := r.files[i]
		if err := restoreRepairFile(path, r.snapshots[path]); err != nil {
			errs = append(errs, err)
		}
	}
	if r.serviceWasLoaded {
		if err := r.manager.Load(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func rollbackRepairError(err error, rollback *repairRollback) error {
	if rollbackErr := rollback.Rollback(); rollbackErr != nil {
		return errors.Join(err, fmt.Errorf("rollback collector service repair: %w", rollbackErr))
	}
	return err
}

func snapshotRepairFile(path string) repairFileSnapshot {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return repairFileSnapshot{}
		}
		return repairFileSnapshot{existed: true, readErr: err}
	}
	snapshot := repairFileSnapshot{existed: true, data: data}
	if info, statErr := os.Stat(path); statErr == nil {
		snapshot.mode = info.Mode().Perm()
	}
	return snapshot
}

func restoreRepairFile(path string, snapshot repairFileSnapshot) error {
	if !snapshot.existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if snapshot.readErr != nil {
		return fmt.Errorf("cannot restore %s: pre-repair snapshot failed: %w", path, snapshot.readErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	mode := snapshot.mode
	if mode == 0 {
		mode = 0600
	}
	return os.WriteFile(path, snapshot.data, mode)
}
