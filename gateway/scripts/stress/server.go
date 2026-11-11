package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var adminClient = &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 4}}

// statSample is one /stats reading, flattened to dotted numeric fields ("runtime.heap_alloc").
type statSample struct {
	at  time.Time
	v   map[string]float64
	err error
}

type sampler struct {
	url     string
	mu      sync.Mutex
	samples []statSample
}

func (s *sampler) fetch(ctx context.Context) statSample {
	ctx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	out := statSample{at: time.Now()}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	resp, err := adminClient.Do(req)
	if err == nil {
		var raw map[string]any
		err = json.NewDecoder(resp.Body).Decode(&raw)
		resp.Body.Close()
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		if err == nil {
			out.v = map[string]float64{}
			flatten("", raw, out.v)
		}
	}
	out.err = err
	s.mu.Lock()
	s.samples = append(s.samples, out)
	s.mu.Unlock()
	return out
}

func flatten(prefix string, v any, out map[string]float64) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if prefix != "" {
				k = prefix + "." + k
			}
			flatten(k, e, out)
		}
	case float64:
		out[prefix] = x
	case bool:
		if x {
			out[prefix] = 1
		} else {
			out[prefix] = 0
		}
	}
}

// sumPrefix sums the fields under prefix ("dropped" sums every output's drops).
func sumPrefix(v map[string]float64, prefix string) float64 {
	var n float64
	for k, x := range v {
		if k == prefix || strings.HasPrefix(k, prefix+".") {
			n += x
		}
	}
	return n
}

// valid returns the successful samples.
func (s *sampler) valid() []statSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []statSample
	for _, x := range s.samples {
		if x.err == nil {
			out = append(out, x)
		}
	}
	return out
}

// ---- client resource use ----

type rusage struct {
	cpu    time.Duration
	maxRSS int64
}

func getRusage() rusage {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	rss := int64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		rss *= 1024 // Linux reports KiB
	}
	return rusage{cpu: time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), maxRSS: rss}
}

// ---- profiles ----

// fetchProfile saves the admin endpoint's /debug/pprof/<path> to dir/file.
func fetchProfile(ctx context.Context, admin, dir, path, file string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := "http://" + admin + "/debug/pprof/" + path
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := adminClient.Do(req)
	if err != nil {
		return fmt.Sprintf("  %-16s FAILED: %v", file, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Sprintf("  %-16s FAILED: %s: %s", file, resp.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.Create(filepath.Join(dir, file))
	if err != nil {
		return fmt.Sprintf("  %-16s FAILED: %v", file, err)
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Sprintf("  %-16s FAILED after %d bytes: %v", file, n, err)
	}
	return fmt.Sprintf("  %-16s %s (%s)", file, bytesStr(float64(n)), path)
}

func bytesStr(n float64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2fGiB", n/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMiB", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKiB", n/(1<<10))
	}
	return fmt.Sprintf("%.0fB", n)
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// slope is the least-squares slope of ys over xs.
func slope(xs, ys []float64) float64 {
	n := float64(len(xs))
	if n < 2 {
		return 0
	}
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}
