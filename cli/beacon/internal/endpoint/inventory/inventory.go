package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

const (
	formatJSON         = "json"
	formatTOML         = "toml"
	formatYAML         = "yaml"
	formatMetadataOnly = "metadata_only"

	KindNativeConfig  = "native_config"
	KindHookConfig    = "hook_config"
	KindPlugin        = "plugin"
	KindProfile       = "profile"
	KindManagedConfig = "managed_config"

	TransportStdio     = "stdio"
	TransportHTTP      = "http"
	TransportSSE       = "sse"
	TransportWebSocket = "websocket"
	TransportUnknown   = "unknown"

	ScopeUser      = "user"
	ScopeProject   = "project"
	ScopeManaged   = "managed"
	ScopeWorkspace = "workspace"
	ScopeSystem    = "system"
	ScopeUnknown   = "unknown"

	StatusOK          = "ok"
	StatusPartial     = "partial"
	StatusParseFailed = "parse_failed"
	StatusNotFound    = "not_found"
	StatusUnreadable  = "unreadable"
	StatusUnsupported = "unsupported"

	RedactionFull = "full"
)

type Options struct {
	HomeDir    string
	WorkingDir string
	Now        func() time.Time
}

type Result struct {
	GeneratedAt string      `json:"generated_at"`
	Configs     []Config    `json:"configs"`
	MCPServers  []MCPServer `json:"mcp_servers"`
	Skills      []Skill     `json:"skills"`
	UserScope   UserScope   `json:"user_scope"`
}

type UserScope struct {
	Mode        string `json:"mode"`
	HomePath    string `json:"home_path,omitempty"`
	HomeHash    string `json:"home_hash,omitempty"`
	WorkingDir  string `json:"working_dir,omitempty"`
	WorkDirHash string `json:"working_dir_hash,omitempty"`
}

type Config struct {
	Runtime        string `json:"runtime"`
	Path           string `json:"path,omitempty"`
	PathHash       string `json:"path_hash,omitempty"`
	Scope          string `json:"scope"`
	ConfigKind     string `json:"config_kind"`
	ParserMode     string `json:"parser_mode"`
	Exists         bool   `json:"exists"`
	Readable       bool   `json:"readable"`
	Reason         string `json:"reason,omitempty"`
	ParserStatus   string `json:"parser_status"`
	FileSHA256     string `json:"file_sha256,omitempty"`
	ModifiedAt     string `json:"modified_at,omitempty"`
	MCPServerCount int    `json:"mcp_server_count"`
	BeaconManaged  bool   `json:"beacon_managed"`
	Redaction      string `json:"redaction"`
}

type MCPServer struct {
	Runtime         string   `json:"runtime"`
	ServerName      string   `json:"server_name,omitempty"`
	ServerNameHash  string   `json:"server_name_hash,omitempty"`
	SourcePath      string   `json:"source_path,omitempty"`
	SourcePathHash  string   `json:"source_path_hash,omitempty"`
	SourceScope     string   `json:"source_scope"`
	Transport       string   `json:"transport"`
	CommandPresent  bool     `json:"command_present"`
	CommandName     string   `json:"command_name,omitempty"`
	CommandNameHash string   `json:"command_name_hash,omitempty"`
	ArgsCount       int      `json:"args_count,omitempty"`
	URLPresent      bool     `json:"url_present"`
	EnvKeys         []string `json:"env_keys,omitempty"`
	EnvKeyCount     int      `json:"env_key_count,omitempty"`
	DefinitionHash  string   `json:"definition_hash"`
	ParserStatus    string   `json:"parser_status"`
	Redaction       string   `json:"redaction"`
}

type candidate struct {
	runtime string
	path    string
	scope   string
	format  string
	kind    string
}

func Scan(opts Options) Result {
	redaction := RedactionFull
	home := opts.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	wd := opts.WorkingDir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	result := Result{
		GeneratedAt: now().UTC().Format(time.RFC3339),
		UserScope: UserScope{
			Mode:        "current_user",
			HomePath:    valueForPath(home, redaction),
			HomeHash:    hashString(home),
			WorkingDir:  valueForPath(wd, redaction),
			WorkDirHash: hashString(wd),
		},
	}
	for _, item := range candidates(home, wd) {
		config, servers := inspectCandidate(item, redaction)
		result.Configs = append(result.Configs, config)
		result.MCPServers = append(result.MCPServers, servers...)
	}
	result.Skills = scanSkills(home, wd, redaction)
	return result
}

func candidates(home, wd string) []candidate {
	items := []candidate{}
	items = append(items, claudeCandidates(home, wd)...)
	items = append(items, codexCandidates(home)...)
	items = append(items, cursorCandidates(home, wd)...)
	items = append(items, geminiCandidates(home)...)
	items = append(items, antigravityCandidates(home, wd)...)
	items = append(items, vscodeCandidates(home, wd)...)
	items = append(items, factoryCandidates(home, wd)...)
	items = append(items, copilotCandidates(home)...)
	items = append(items, opencodeCandidates(home, wd)...)
	items = append(items, hermesCandidates(home)...)
	items = append(items, devinCandidates(home, wd)...)
	items = append(items, grokCandidates(home, wd)...)
	seen := map[string]bool{}
	out := make([]candidate, 0, len(items))
	for _, item := range items {
		key := item.runtime + "\x00" + item.path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func claudeCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "claude_code", path: filepath.Join(home, ".claude", "settings.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "claude_code", path: filepath.Join(wd, ".claude", "settings.json"), scope: ScopeProject, format: formatJSON, kind: KindNativeConfig},
		{runtime: "claude_code", path: "/Library/Application Support/ClaudeCode/managed-settings.json", scope: ScopeManaged, format: formatJSON, kind: KindManagedConfig},
	}
}

func codexCandidates(home string) []candidate {
	return []candidate{{runtime: "codex_cli", path: filepath.Join(home, ".codex", "config.toml"), scope: ScopeUser, format: formatTOML, kind: KindNativeConfig}}
}

func cursorCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "mcp.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "cursor", path: filepath.Join(wd, ".cursor", "mcp.json"), scope: ScopeProject, format: formatJSON, kind: KindNativeConfig},
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "hooks.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "cursor", path: filepath.Join(wd, ".cursor", "hooks.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
	}
}

func geminiCandidates(home string) []candidate {
	return []candidate{{runtime: "gemini_cli", path: filepath.Join(home, ".gemini", "settings.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig}}
}

func antigravityCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "antigravity_cli", path: filepath.Join(home, ".gemini", "config", "hooks.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "antigravity_cli", path: filepath.Join(wd, ".agents", "hooks.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
	}
}

func vscodeCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "vscode", path: vscodeUserSettingsPath(home), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "vscode", path: filepath.Join(wd, ".vscode", "settings.json"), scope: ScopeProject, format: formatJSON, kind: KindNativeConfig},
		{runtime: "vscode", path: filepath.Join(home, ".copilot", "hooks", "beacon.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "vscode", path: filepath.Join(wd, ".github", "hooks", "beacon.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
	}
}

func factoryCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "factory", path: shellProfilePath(home), scope: ScopeUser, format: formatMetadataOnly, kind: KindProfile},
		{runtime: "factory", path: filepath.Join(home, ".factory", "settings.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "factory", path: filepath.Join(wd, ".factory", "settings.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
	}
}

func copilotCandidates(home string) []candidate {
	return []candidate{{runtime: "copilot_cli", path: shellProfilePath(home), scope: ScopeUser, format: formatMetadataOnly, kind: KindProfile}}
}

func opencodeCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "opencode", path: filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts"), scope: ScopeUser, format: formatMetadataOnly, kind: KindPlugin},
		{runtime: "opencode", path: filepath.Join(wd, ".opencode", "plugins", "beacon.ts"), scope: ScopeProject, format: formatMetadataOnly, kind: KindPlugin},
	}
}

func hermesCandidates(home string) []candidate {
	return []candidate{{runtime: "hermes", path: filepath.Join(home, ".hermes", "config.yaml"), scope: ScopeUser, format: formatYAML, kind: KindNativeConfig}}
}

func devinCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "devin-cli", path: filepath.Join(home, ".config", "devin", "config.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "devin-cli", path: filepath.Join(wd, ".devin", "hooks.v1.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
		{runtime: "devin-desktop", path: filepath.Join(home, ".codeium", "windsurf", "hooks.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "devin-desktop", path: filepath.Join(wd, ".windsurf", "hooks.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
	}
}

func grokCandidates(home, wd string) []candidate {
	return []candidate{
		{runtime: "grok", path: filepath.Join(home, ".grok", "hooks", "beacon-endpoint.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "grok", path: filepath.Join(wd, ".grok", "hooks", "beacon-endpoint.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
	}
}

func vscodeUserSettingsPath(home string) string {
	switch runtime.GOOS {
	case "linux":
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "Code", "User", "settings.json")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Code", "User", "settings.json")
		}
		return filepath.Join(home, "AppData", "Roaming", "Code", "User", "settings.json")
	}
	return filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json")
}

func shellProfilePath(home string) string {
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		return filepath.Join(home, ".bash_profile")
	default:
		return filepath.Join(home, ".profile")
	}
}

func inspectCandidate(item candidate, redaction string) (Config, []MCPServer) {
	config := Config{
		Runtime:      item.runtime,
		Path:         valueForPath(item.path, redaction),
		PathHash:     hashString(item.path),
		Scope:        item.scope,
		ConfigKind:   item.kind,
		ParserMode:   item.format,
		ParserStatus: StatusNotFound,
		Redaction:    redaction,
	}
	info, err := os.Stat(item.path)
	if err != nil {
		config.Reason = errReason(err)
		if !os.IsNotExist(err) {
			config.ParserStatus = StatusUnreadable
		}
		return config, nil
	}
	config.Exists = true
	config.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
	if info.IsDir() {
		config.ParserStatus = StatusUnsupported
		config.Reason = "path is a directory"
		return config, nil
	}
	data, err := os.ReadFile(item.path)
	if err != nil {
		config.ParserStatus = StatusUnreadable
		config.Reason = errReason(err)
		return config, nil
	}
	config.Readable = true
	config.FileSHA256 = hashBytes(data)
	config.BeaconManaged = beaconManaged(item, data)

	servers, parseErr := parseMCPServers(item, data, redaction)
	config.MCPServerCount = len(servers)
	config.ParserStatus = StatusOK
	if parseErr != nil {
		config.ParserStatus = StatusParseFailed
		config.Reason = parseErr.Error()
	}
	return config, servers
}

func parseMCPServers(item candidate, data []byte, redaction string) ([]MCPServer, error) {
	switch item.format {
	case formatMetadataOnly:
		return nil, nil
	case formatJSON:
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, err
		}
		return serversFromMap(item, root, redaction), nil
	case formatTOML:
		var root map[string]interface{}
		if err := toml.Unmarshal(data, &root); err != nil {
			return nil, err
		}
		return serversFromMap(item, root, redaction), nil
	case formatYAML:
		var root map[string]interface{}
		if err := yaml.Unmarshal(data, &root); err != nil {
			return nil, err
		}
		return serversFromMap(item, root, redaction), nil
	default:
		return nil, fmt.Errorf("unsupported config format %q", item.format)
	}
}

func serversFromMap(item candidate, root map[string]interface{}, redaction string) []MCPServer {
	var servers []MCPServer
	for _, key := range []string{"mcpServers", "mcp_servers", "servers"} {
		if raw, ok := root[key]; ok {
			servers = append(servers, serversFromBlock(item, raw, redaction)...)
		}
	}
	if raw, ok := root["mcp"]; ok {
		servers = append(servers, serversFromBlock(item, raw, redaction)...)
	}
	return dedupeServers(servers)
}

func serversFromBlock(item candidate, raw interface{}, redaction string) []MCPServer {
	block, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	var servers []MCPServer
	for name, value := range block {
		def, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		if looksLikeServerDefinition(def) {
			servers = append(servers, serverFromDefinition(item, name, def, redaction))
			continue
		}
		for nestedName, nestedValue := range def {
			nestedDef, ok := nestedValue.(map[string]interface{})
			if ok && looksLikeServerDefinition(nestedDef) {
				servers = append(servers, serverFromDefinition(item, nestedName, nestedDef, redaction))
			}
		}
	}
	return servers
}

func looksLikeServerDefinition(def map[string]interface{}) bool {
	for _, key := range []string{"command", "args", "env", "url", "transport"} {
		if _, ok := def[key]; ok {
			return true
		}
	}
	return false
}

func serverFromDefinition(item candidate, name string, def map[string]interface{}, redaction string) MCPServer {
	command := firstString(def["command"])
	url := firstString(def["url"])
	envKeys := mapKeys(def["env"])
	server := MCPServer{
		Runtime:        item.runtime,
		ServerName:     valueForName(name, redaction),
		ServerNameHash: hashString(name),
		SourcePath:     valueForPath(item.path, redaction),
		SourcePathHash: hashString(item.path),
		SourceScope:    item.scope,
		Transport:      inferTransport(def, url, command),
		CommandPresent: command != "",
		CommandName:    valueForName(filepath.Base(command), redaction),
		ArgsCount:      sliceLen(def["args"]),
		URLPresent:     url != "",
		EnvKeys:        valuesForEnvKeys(envKeys, redaction),
		EnvKeyCount:    len(envKeys),
		DefinitionHash: "sha256:" + canonicalHash(def),
		ParserStatus:   StatusOK,
		Redaction:      redaction,
	}
	if command != "" {
		server.CommandNameHash = hashString(filepath.Base(command))
	}
	return server
}

func inferTransport(def map[string]interface{}, url, command string) string {
	transport := strings.ToLower(firstString(def["transport"]))
	switch transport {
	case TransportStdio, TransportHTTP, TransportSSE, TransportWebSocket:
		return transport
	}
	lowerURL := strings.ToLower(url)
	switch {
	case strings.HasPrefix(lowerURL, "ws://"), strings.HasPrefix(lowerURL, "wss://"):
		return TransportWebSocket
	case strings.Contains(lowerURL, "sse"):
		return TransportSSE
	case strings.HasPrefix(lowerURL, "http://"), strings.HasPrefix(lowerURL, "https://"):
		return TransportHTTP
	case command != "":
		return TransportStdio
	default:
		return TransportUnknown
	}
}

func dedupeServers(servers []MCPServer) []MCPServer {
	seen := map[string]bool{}
	out := make([]MCPServer, 0, len(servers))
	for _, server := range servers {
		key := server.Runtime + "\x00" + server.SourcePathHash + "\x00" + server.ServerNameHash + "\x00" + server.DefinitionHash
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, server)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runtime == out[j].Runtime {
			if out[i].SourcePathHash == out[j].SourcePathHash {
				return out[i].ServerNameHash < out[j].ServerNameHash
			}
			return out[i].SourcePathHash < out[j].SourcePathHash
		}
		return out[i].Runtime < out[j].Runtime
	})
	return out
}

func valueForPath(value, redaction string) string {
	return value
}

func valueForName(value, redaction string) string {
	return value
}

func valuesForEnvKeys(keys []string, redaction string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if safeEnvKey(key) {
			out = append(out, key)
		}
	}
	return out
}

func safeEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "AUTH", "CREDENTIAL"} {
		if strings.Contains(upper, marker) {
			return false
		}
	}
	return true
}

func mapKeys(raw interface{}) []string {
	values, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sliceLen(raw interface{}) int {
	values, ok := raw.([]interface{})
	if !ok {
		return 0
	}
	return len(values)
}

func firstString(raw interface{}) string {
	switch typed := raw.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func beaconManaged(item candidate, data []byte) bool {
	text := string(data)
	switch item.runtime {
	case "claude_code", "cursor", "antigravity_cli", "vscode", "factory", "hermes", "devin-cli", "devin-desktop":
		if strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
			return true
		}
	case "opencode":
		return strings.Contains(text, "beacon-managed-opencode-plugin:v1")
	case "grok":
		return strings.Contains(text, "beacon-managed-grok-hooks:v1")
	}
	if item.runtime == "claude_code" || item.runtime == "codex_cli" {
		if strings.Contains(text, "OTEL_EXPORTER_OTLP_ENDPOINT") && localEndpointText(text) {
			return true
		}
	}
	if item.runtime == "gemini_cli" && strings.Contains(text, "otlpEndpoint") && localEndpointText(text) {
		return true
	}
	if item.runtime == "vscode" && strings.Contains(text, "github.copilot.chat.otel.otlpEndpoint") && localEndpointText(text) {
		return true
	}
	if item.runtime == "factory" {
		if ep := shellExportValue(text, "OTEL_TELEMETRY_ENDPOINT"); ep != "" && localEndpointText(ep) {
			return true
		}
	}
	if item.runtime == "copilot_cli" && copilotOTELEnabled(text) {
		return true
	}
	return false
}

func copilotOTELEnabled(text string) bool {
	enabled := shellExportValue(text, "COPILOT_OTEL_ENABLED")
	if !truthyValue(enabled) {
		return false
	}
	endpoint := shellExportValue(text, "COPILOT_OTEL_ENDPOINT")
	if endpoint == "" {
		endpoint = shellExportValue(text, "OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint == "" {
		return false
	}
	return localEndpointText(endpoint)
}

func shellExportValue(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		return strings.Trim(value, `"'`)
	}
	return ""
}

func truthyValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "true" || normalized == "1"
}

func localEndpointText(text string) bool {
	return strings.Contains(text, "127.0.0.1") || strings.Contains(text, "localhost")
}

func hashString(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalHash(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return hashString(fmt.Sprint(value))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func errReason(err error) string {
	if err == nil {
		return ""
	}
	if os.IsNotExist(err) {
		return "not found"
	}
	if os.IsPermission(err) {
		return "permission denied"
	}
	return err.Error()
}
