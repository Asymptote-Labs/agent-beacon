package dshsession

import (
	"strconv"
	"strings"
)

type dshToolKind int

const (
	dshToolOther dshToolKind = iota
	dshToolRead
	dshToolCreate
	dshToolModify
	dshToolShell
)

var dshToolKinds = map[string]dshToolKind{
	"read":       dshToolRead,
	"read_image": dshToolRead,
	"glob":       dshToolRead,
	"grep":       dshToolRead,

	"write":              dshToolCreate,
	"edit":               dshToolModify,
	"str_replace_editor": dshToolCreate,

	"bash":          dshToolShell,
	"pwsh":          dshToolShell,
	"terminal_send": dshToolShell,

	"terminal_open":   dshToolOther,
	"terminal_read":   dshToolOther,
	"terminal_list":   dshToolOther,
	"terminal_close":  dshToolOther,
	"terminal_signal": dshToolOther,
	"job_output":      dshToolOther,
	"job_list":        dshToolOther,
	"job_kill":        dshToolOther,

	"run_code":                    dshToolOther,
	"ask_user_question":           dshToolOther,
	"exit_plan_mode":              dshToolOther,
	"present":                     dshToolOther,
	"plugin_manager":              dshToolOther,
	"lsp":                         dshToolOther,
	"ralph":                       dshToolOther,
	"skill":                       dshToolOther,
	"workflow":                    dshToolOther,
	"todo_write":                  dshToolOther,
	"web_fetch":                   dshToolOther,
	"web_search":                  dshToolOther,
	"create_goal":                 dshToolOther,
	"get_goal":                    dshToolOther,
	"update_goal":                 dshToolOther,
	"schedule_create":             dshToolOther,
	"schedule_delete":             dshToolOther,
	"schedule_list":               dshToolOther,
	"subagent":                    dshToolOther,
	"list_subagent_models":        dshToolOther,
	"spawn_teammate":              dshToolOther,
	"wait_agent":                  dshToolOther,
	"interrupt_agent":             dshToolOther,
	"list_agents":                 dshToolOther,
	"send_message":                dshToolOther,
	"team_task_create":            dshToolOther,
	"team_task_get":               dshToolOther,
	"team_task_list":              dshToolOther,
	"team_task_update":            dshToolOther,
	"session_search":              dshToolOther,
	"session_trace":               dshToolOther,
	"session_event_read":          dshToolOther,
	"session_event_search":        dshToolOther,
	"session_event_trace":         dshToolOther,
	"cordis_inspect_list":         dshToolOther,
	"cordis_inspect_query":        dshToolOther,
	"stagehand_act":               dshToolOther,
	"stagehand_extract":           dshToolOther,
	"stagehand_navigate":          dshToolOther,
	"stagehand_observe":           dshToolOther,
	"stagehand_screenshot":        dshToolOther,
	"stagehand_tabs":              dshToolOther,
	"list_mcp_resources":          dshToolOther,
	"list_mcp_resource_templates": dshToolOther,
	"read_mcp_resource":           dshToolOther,
}

var dshEditorCommands = map[string]bool{
	"view":        true,
	"create":      true,
	"str_replace": true,
	"insert":      true,
}

const dshExitCodeMarkerPrefix = "[exit code:"

func dshToolAction(toolName string, toolInput map[string]interface{}) string {
	kind, known := dshEffectiveToolKind(toolName, toolInput)
	if !known {
		return ""
	}
	switch kind {
	case dshToolRead:
		return "file.read"
	case dshToolCreate, dshToolModify:
		return "file.modified"
	case dshToolShell:
		return "command.executed"
	default:
		return "tool.invoked"
	}
}

func dshFileOperation(toolName string, toolInput map[string]interface{}) string {
	kind, known := dshEffectiveToolKind(toolName, toolInput)
	if !known {
		return ""
	}
	switch kind {
	case dshToolRead:
		return "read"
	case dshToolCreate:
		return "create"
	case dshToolModify:
		return "modify"
	default:
		return ""
	}
}

func dshEffectiveToolKind(toolName string, toolInput map[string]interface{}) (dshToolKind, bool) {
	kind, known := dshToolKinds[strings.ToLower(strings.TrimSpace(toolName))]
	if !known {
		return dshToolOther, false
	}
	if operation := dshEditorOperation(toolName, toolInput); operation != "" {
		kind = dshEditorKindForOperation(operation)
	}
	return kind, true
}

func dshEditorOperation(toolName string, toolInput map[string]interface{}) string {
	if strings.ToLower(strings.TrimSpace(toolName)) != "str_replace_editor" {
		return ""
	}
	value := strings.ToLower(strings.TrimSpace(firstString(toolInput, "command")))
	if !dshEditorCommands[value] {
		return ""
	}
	return value
}

func dshEditorKindForOperation(operation string) dshToolKind {
	switch operation {
	case "view":
		return dshToolRead
	case "create":
		return dshToolCreate
	default:
		return dshToolModify
	}
}

func commandFromCall(call toolCall) string {
	switch strings.ToLower(strings.TrimSpace(call.Name)) {
	case "terminal_send":
		return firstString(call.Arguments, "text")
	default:
		return firstString(call.Arguments, "command", "cmd")
	}
}

func dshExitCode(output string) (int, bool) {
	index := strings.LastIndex(output, dshExitCodeMarkerPrefix)
	if index < 0 {
		return 0, false
	}
	rest := output[index+len(dshExitCodeMarkerPrefix):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		return 0, false
	}
	return code, true
}
