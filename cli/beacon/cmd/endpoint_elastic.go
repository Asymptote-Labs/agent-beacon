package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/elastic"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

func runEndpointElasticUp(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("beacon endpoint elastic up is currently macOS-only")
	}
	cfg := loadOrDefaultConfig()
	logPath, err := filepath.Abs(cfg.LogPath)
	if err != nil {
		return err
	}
	packDir := endpointOpts.elasticPackDir
	if packDir == "" {
		packDir = elastic.DefaultOutputDir
	}
	if err := ensureElasticPack(packDir, logPath); err != nil {
		return err
	}
	if err := ensureLogFile(logPath); err != nil {
		return err
	}
	env := os.Environ()
	env = append(env, "BEACON_LOG_DIR="+filepath.Dir(logPath))
	if err := runDockerCompose(ctx, packDir, env, "up", "-d"); err != nil {
		return err
	}
	fmt.Printf("Elasticsearch ready at http://localhost:%s\n", envDefault("BEACON_ELASTIC_ES_PORT", "9200"))
	fmt.Printf("Kibana ready at http://localhost:%s\n", envDefault("BEACON_ELASTIC_KIBANA_PORT", "5601"))
	fmt.Printf("Filebeat tailing %s\n", logPath)
	return nil
}

func runEndpointElasticDown(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("beacon endpoint elastic down is currently macOS-only")
	}
	packDir := endpointOpts.elasticPackDir
	if packDir == "" {
		packDir = elastic.DefaultOutputDir
	}
	if _, err := os.Stat(filepath.Join(packDir, "docker-compose.yml")); os.IsNotExist(err) {
		fmt.Printf("No Elasticsearch stack found for %s\n", packDir)
		return nil
	} else if err != nil {
		return err
	}
	logPath, err := filepath.Abs(loadOrDefaultConfig().LogPath)
	if err != nil {
		return err
	}
	env := append(os.Environ(), "BEACON_LOG_DIR="+filepath.Dir(logPath))
	if err := runDockerCompose(ctx, packDir, env, "down", "--remove-orphans"); err != nil {
		return err
	}
	fmt.Printf("Elasticsearch stack stopped for %s\n", packDir)
	return nil
}

func ensureElasticPack(packDir, logPath string) error {
	if _, err := os.Stat(filepath.Join(packDir, "docker-compose.yml")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := elastic.InstallPack(packDir, logPath); err != nil {
		return err
	}
	fmt.Printf("Elasticsearch content pack written to %s\n", packDir)
	return nil
}

func ensureLogFile(path string) error {
	if path == "" {
		return nil
	}
	return writer.EnsureRuntimeFile(path)
}

func runDockerCompose(ctx context.Context, dir string, env []string, args ...string) error {
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		return fmt.Errorf("docker-compose.yml not found in %s: %w", dir, err)
	}
	fullArgs := append([]string{"compose"}, args...)
	cmd := exec.CommandContext(ctx, "docker", fullArgs...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func envDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}
