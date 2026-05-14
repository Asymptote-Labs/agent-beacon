package cmd

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/cowork"
)

const (
	defaultCoworkProtocol                = "HTTP/protobuf"
	defaultCoworkProdResourceAttributes  = "deployment.environment=prod,service.name=claude-cowork"
	defaultCoworkLocalResourceAttributes = "deployment.environment=local,service.name=claude-cowork"
	defaultCoworkTunnelUser              = "beacon"
	defaultCoworkNgrokStartupTimeout     = 20 * time.Second
)

var ngrokURLPattern = regexp.MustCompile(`url="?((?:https)://[^"\s]+)`)

func runEndpointCoworkSetup(cmdCtx context.Context) error {
	cfg := loadOrDefaultConfig()
	resourceAttributes := endpointOpts.coworkResourceAttributes
	if endpointOpts.coworkNgrok {
		if endpointOpts.coworkEndpoint != "" {
			return fmt.Errorf("--endpoint cannot be combined with --ngrok")
		}
		if resourceAttributes == "" {
			resourceAttributes = defaultCoworkLocalResourceAttributes
		}
		if err := requireLocalOTLPHTTP(cfg.Collector.HTTPPort); err != nil {
			return err
		}
		password, err := randomTunnelPassword()
		if err != nil {
			return err
		}
		tunnel, err := startNgrokTunnel(cmdCtx, cfg.Collector.HTTPPort, defaultCoworkTunnelUser, password)
		if err != nil {
			return err
		}
		headers := basicAuthHeader(defaultCoworkTunnelUser, password)
		printCoworkSetup(cowork.Config{
			Endpoint:           tunnel.URL,
			Protocol:           defaultCoworkProtocol,
			Headers:            headers,
			ResourceAttributes: resourceAttributes,
		})
		if endpointOpts.coworkOpen {
			if err := dashboard.OpenBrowser(cowork.AdminURL); err != nil {
				_ = tunnel.Stop()
				return err
			}
		}
		fmt.Println("Temporary ngrok tunnel is running. Press Ctrl-C to stop it.")
		return tunnel.Wait()
	}

	if endpointOpts.coworkEndpoint == "" {
		return fmt.Errorf("--endpoint is required unless --ngrok is set")
	}
	if !strings.HasPrefix(strings.ToLower(endpointOpts.coworkEndpoint), "https://") {
		return fmt.Errorf("--endpoint must be a public HTTPS URL reachable by Claude Cowork")
	}
	if resourceAttributes == "" {
		resourceAttributes = defaultCoworkProdResourceAttributes
	}
	printCoworkSetup(cowork.Config{
		Endpoint:           endpointOpts.coworkEndpoint,
		Protocol:           defaultCoworkProtocol,
		Headers:            endpointOpts.coworkHeaders,
		ResourceAttributes: resourceAttributes,
	})
	if endpointOpts.coworkOpen {
		return dashboard.OpenBrowser(cowork.AdminURL)
	}
	return nil
}

func runEndpointCoworkValidate() error {
	cfg := loadOrDefaultConfig()
	status := cowork.GetStatus(cfg.LogPath)
	if endpointOpts.coworkSince != "" {
		duration, err := time.ParseDuration(endpointOpts.coworkSince)
		if err != nil {
			return fmt.Errorf("--since must be a duration such as 10m: %w", err)
		}
		since := time.Now().Add(-duration)
		if !cowork.HasCoworkEventSince(cfg.LogPath, since) {
			fmt.Print(cowork.PrintConfig(cowork.Config{
				Endpoint:           endpointOpts.coworkEndpoint,
				Protocol:           defaultCoworkProtocol,
				Headers:            endpointOpts.coworkHeaders,
				ResourceAttributes: endpointOpts.coworkResourceAttributes,
			}))
			return fmt.Errorf("no Claude Cowork events observed in %s since %s", cfg.LogPath, since.UTC().Format(time.RFC3339))
		}
		fmt.Printf("Claude Cowork events observed in endpoint runtime log since %s.\n", since.UTC().Format(time.RFC3339))
		return nil
	}
	if !status.LastEventObserved {
		fmt.Print(cowork.PrintConfig(cowork.Config{
			Endpoint:           endpointOpts.coworkEndpoint,
			Protocol:           defaultCoworkProtocol,
			Headers:            endpointOpts.coworkHeaders,
			ResourceAttributes: endpointOpts.coworkResourceAttributes,
		}))
		return fmt.Errorf("no Claude Cowork events observed in %s", cfg.LogPath)
	}
	if status.LastEventObservedAt != "" {
		fmt.Printf("Claude Cowork events observed in endpoint runtime log. Last observed: %s.\n", status.LastEventObservedAt)
	} else {
		fmt.Println("Claude Cowork events observed in endpoint runtime log.")
	}
	return nil
}

func printCoworkSetup(cfg cowork.Config) {
	fmt.Print(cowork.PrintConfig(cfg))
	fmt.Println("Copy these values into the Claude Cowork monitoring settings and save:")
	fmt.Printf("  OTLP endpoint: %s\n", cfg.Endpoint)
	fmt.Printf("  OTLP protocol: %s\n", cfg.Protocol)
	fmt.Printf("  OTLP headers: %s\n", displayValue(cfg.Headers))
	fmt.Printf("  Resource attributes: %s\n", displayValue(cfg.ResourceAttributes))
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

func requireLocalOTLPHTTP(port int) error {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return fmt.Errorf("local OTLP HTTP receiver is not listening on 127.0.0.1:%d; run `beacon endpoint install` or `beacon endpoint repair` first", port)
	}
	_ = conn.Close()
	return nil
}

type ngrokTunnel struct {
	URL string
	cmd *exec.Cmd
}

func startNgrokTunnel(ctx context.Context, port int, username, password string) (*ngrokTunnel, error) {
	if _, err := exec.LookPath("ngrok"); err != nil {
		return nil, fmt.Errorf("ngrok was not found on PATH; install ngrok or use --endpoint with a public HTTPS collector")
	}
	cmd := exec.CommandContext(ctx, "ngrok", "http", strconv.Itoa(port), "--basic-auth", username+":"+password, "--log", "stdout", "--log-format", "logfmt")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	urlCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		reportedURL := false
		for scanner.Scan() {
			line := scanner.Text()
			if url := parseNgrokURL(line); url != "" && !reportedURL {
				reportedURL = true
				urlCh <- url
			}
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
			return
		}
		errCh <- fmt.Errorf("ngrok exited before reporting a public URL")
	}()
	select {
	case url := <-urlCh:
		return &ngrokTunnel{URL: url, cmd: cmd}, nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	case <-time.After(defaultCoworkNgrokStartupTimeout):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("timed out waiting for ngrok public URL")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ctx.Err()
	}
}

func (t *ngrokTunnel) Wait() error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	wait := make(chan error, 1)
	go func() {
		wait <- t.cmd.Wait()
	}()
	select {
	case <-signals:
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Signal(os.Interrupt)
		}
		err := <-wait
		if err != nil {
			return nil
		}
		return nil
	case err := <-wait:
		return err
	}
}

func (t *ngrokTunnel) Stop() error {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	return t.cmd.Process.Kill()
}

func parseNgrokURL(line string) string {
	matches := ngrokURLPattern.FindStringSubmatch(line)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func randomTunnelPassword() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func basicAuthHeader(username, password string) string {
	token := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return "Authorization=Basic " + token
}
