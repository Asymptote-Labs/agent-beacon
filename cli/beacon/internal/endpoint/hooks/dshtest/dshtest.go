// Package dshtest builds on-disk DeepSeek Harness fixtures for tests: a dsh CLI package reachable
// on PATH, and profiles with the Claude Code hook bridge installed, so code that inspects whether
// dsh can load the bridge runs against files rather than a seam.
package dshtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	BridgePackage   = "@deepseek-ai/dsh-hooks-claude-code"
	ProtocolPackage = "@deepseek-ai/dsh-hook-protocol"
)

// HostPeers are the bridge's dsh peers that the dsh installation ships, a representative subset of
// what the real bridge declares.
var HostPeers = []string{"@deepseek-ai/dsh-agent", "@deepseek-ai/dsh-llm"}

// InstallCLI writes a dsh CLI package of the given version under a temporary directory and puts a
// `dsh` launcher that links to its lib/bin.js first on PATH, the shape of an npm global install.
// It returns the package directory.
func InstallCLI(t testing.TB, version string) string {
	t.Helper()
	root := t.TempDir()
	pkg := filepath.Join(root, "lib", "node_modules", "@deepseek-ai", "dsh")
	WritePackage(t, pkg, "@deepseek-ai/dsh", version)
	for _, peer := range HostPeers {
		WritePackage(t, filepath.Join(pkg, "node_modules", filepath.FromSlash(peer)), peer, version)
	}
	binJS := filepath.Join(pkg, "lib", "bin.js")
	if err := os.MkdirAll(filepath.Dir(binJS), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binJS, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binJS, filepath.Join(bin, "dsh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return pkg
}

// InstallBridge installs the bridge and its protocol peer at version into a profile under dshHome,
// as `dsh plugin --profile <profile> add` leaves them, and returns the profile directory.
func InstallBridge(t testing.TB, dshHome, profile, bridgeVersion, protocolVersion string) string {
	t.Helper()
	dir := filepath.Join(dshHome, "profiles", profile)
	WriteBridge(t, filepath.Join(dir, "node_modules", filepath.FromSlash(BridgePackage)), bridgeVersion)
	if protocolVersion != "" {
		WritePackage(t, filepath.Join(dir, "node_modules", filepath.FromSlash(ProtocolPackage)), ProtocolPackage, protocolVersion)
	}
	return dir
}

// Profile creates an empty profile directory under dshHome.
func Profile(t testing.TB, dshHome, profile string) string {
	t.Helper()
	dir := filepath.Join(dshHome, "profiles", profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// WriteBridge writes a bridge package at dir that declares its peers, pinned to version, the way
// the published bridge does.
func WriteBridge(t testing.TB, dir, version string) {
	t.Helper()
	peers := map[string]string{ProtocolPackage: version}
	for _, peer := range HostPeers {
		peers[peer] = version
	}
	writeManifest(t, dir, map[string]interface{}{"name": BridgePackage, "version": version, "peerDependencies": peers})
}

// WritePackage writes a package.json naming a package at dir.
func WritePackage(t testing.TB, dir, name, version string) {
	t.Helper()
	writeManifest(t, dir, map[string]interface{}{"name": name, "version": version})
}

func writeManifest(t testing.TB, dir string, manifest map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Ready installs a dsh CLI at version and a matching bridge in profile "web" under dshHome, the
// fixture a test needs when it only wants the install to proceed.
func Ready(t testing.TB, dshHome, version string) {
	t.Helper()
	InstallCLI(t, version)
	InstallBridge(t, dshHome, "web", version, version)
}
