package main

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	rtdebug "runtime/debug"
	"strconv"
	"strings"
)

// gc tunes the garbage collector. The call path allocates quickly against a small live heap, so
// at Go's default (GOGC=100) the collector runs constantly: marking was a quarter of all CPU under
// load, and a higher percentage made the gateway a quarter faster. The memory limit caps what that
// trade can cost: near it, the collector runs as often as it must. GOGC and GOMEMLIMIT in the
// environment win over both.
type gc struct {
	// Percent is heap growth over the live heap before a collection, as GOGC (default 400).
	Percent int `yaml:"percent"`
	// MemoryLimit is a soft limit on the process's memory, in GOMEMLIMIT's syntax ("6GiB").
	// Default: 80% of the container's memory limit (cgroup v2) when there is one, else none.
	MemoryLimit string `yaml:"memory_limit"`
}

const defaultGCPercent = 400

// apply sets the collector up and returns the memory limit in effect, 0 when there is none.
func (g gc) apply(log *slog.Logger) (int64, error) {
	percent, percentFrom := g.Percent, "config"
	if percent == 0 {
		percent, percentFrom = defaultGCPercent, "default"
	}
	if v := os.Getenv("GOGC"); v != "" {
		percentFrom = "GOGC=" + v
	} else {
		rtdebug.SetGCPercent(percent)
	}

	limit, limitFrom := int64(math.MaxInt64), "none"
	switch {
	case os.Getenv("GOMEMLIMIT") != "":
		limitFrom = "GOMEMLIMIT=" + os.Getenv("GOMEMLIMIT")
	case g.MemoryLimit != "":
		n, err := parseBytes(g.MemoryLimit)
		if err != nil {
			return 0, fmt.Errorf("gc.memory_limit: %w", err)
		}
		limit, limitFrom = n, "config"
	default:
		if n, ok := cgroupMemoryMax(); ok {
			limit, limitFrom = n/10*8, "80% of cgroup memory.max"
		}
	}
	if strings.HasPrefix(limitFrom, "GOMEMLIMIT") {
		limit = rtdebug.SetMemoryLimit(-1) // reads it
	} else {
		rtdebug.SetMemoryLimit(limit)
	}
	log.Info("gc", "percent", percent, "percent_from", percentFrom, "memory_limit", limit, "memory_limit_from", limitFrom)
	if limit == math.MaxInt64 {
		return 0, nil
	}
	return limit, nil
}

// cgroupMemoryMax is the cgroup v2 memory limit of the process's container, if it has one.
func cgroupMemoryMax() (int64, bool) {
	raw, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return n, err == nil && n > 0
}

// parseBytes reads a size as GOMEMLIMIT does: an integer with an optional B, KiB, MiB, GiB or TiB.
func parseBytes(s string) (int64, error) {
	units := []struct {
		suffix string
		scale  int64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}
	scale := int64(1)
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			s, scale = strings.TrimSuffix(s, u.suffix), u.scale
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/scale {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * scale, nil
}
