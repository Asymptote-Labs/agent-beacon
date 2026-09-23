package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// settingsHookGroup and settingsHookRef model the shape Beacon writes into a runtime's shared
// settings file. That file also holds the user's own hooks, including kinds Beacon has no fields
// for: Claude Code's `http` hooks carry `url`, `headers` and `allowedEnvVars`, `prompt` and `agent`
// hooks carry `prompt` and `model`, and command hooks can carry `async` or `statusMessage`. A
// rewrite that decoded those into these structs and encoded them back dropped every field it did
// not model and added an empty `command`, which left a user's http guard hook in place but inert
// (#618). So an entry read from disk remembers its original JSON and is written back from it; see
// preserveUnmodelledJSON.
type settingsHookGroup struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []settingsHookRef `json:"hooks"`

	raw json.RawMessage
}

type settingsHookRef struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
	// Shell names the interpreter the runtime should parse Command with, for runtimes that let a
	// hook say. Omitted by default, so the runtimes that do not expose the field are unaffected.
	//
	// It exists because Command is quoted for one specific shell -- see hookCommandQuote -- and a
	// runtime that picks a different one cannot run it at all. Saying which shell the command was
	// written for is strictly better than emitting a second quoting dialect and hoping the runtime
	// picks the matching one.
	Shell      string `json:"shell,omitempty"`
	ShowOutput *bool  `json:"show_output,omitempty"`

	raw json.RawMessage
}

// The alias types have the same fields and no methods, so the custom methods below can use the
// default encoding without recursing into themselves.
type settingsHookGroupFields settingsHookGroup
type settingsHookRefFields settingsHookRef

func (group *settingsHookGroup) UnmarshalJSON(data []byte) error {
	var fields settingsHookGroupFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*group = settingsHookGroup(fields)
	group.raw = bytes.Clone(data)
	return nil
}

func (group settingsHookGroup) MarshalJSON() ([]byte, error) {
	current := settingsHookGroupFields(group)
	if group.raw == nil {
		return json.Marshal(current)
	}
	var original settingsHookGroupFields
	if err := json.Unmarshal(group.raw, &original); err != nil {
		return nil, err
	}
	return preserveUnmodelledJSON(group.raw, original, current)
}

func (hook *settingsHookRef) UnmarshalJSON(data []byte) error {
	var fields settingsHookRefFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*hook = settingsHookRef(fields)
	hook.raw = bytes.Clone(data)
	return nil
}

func (hook settingsHookRef) MarshalJSON() ([]byte, error) {
	current := settingsHookRefFields(hook)
	if hook.raw == nil {
		return json.Marshal(current)
	}
	var original settingsHookRefFields
	if err := json.Unmarshal(hook.raw, &original); err != nil {
		return nil, err
	}
	return preserveUnmodelledJSON(hook.raw, original, current)
}

// preserveUnmodelledJSON re-encodes an object Beacon read from disk. raw is the object as it was
// read, original is raw decoded into Beacon's struct, and current is that struct now. Every field
// whose encoding is unchanged keeps raw's spelling, including whether it was present at all, so an
// object nobody edited comes back byte for byte and one that was edited keeps every field Beacon
// does not model.
func preserveUnmodelledJSON(raw json.RawMessage, original, current any) ([]byte, error) {
	before, err := jsonObjectFields(original)
	if err != nil {
		return nil, err
	}
	after, err := jsonObjectFields(current)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	edited := false
	for _, fields := range []map[string]json.RawMessage{before, after} {
		for key := range fields {
			was, hadBefore := before[key]
			now, hasAfter := after[key]
			if hadBefore == hasAfter && bytes.Equal(was, now) {
				continue
			}
			if !edited {
				if err := json.Unmarshal(raw, &out); err != nil {
					return nil, err
				}
				edited = true
			}
			if hasAfter {
				out[key] = now
			} else {
				delete(out, key)
			}
		}
	}
	if !edited {
		return raw, nil
	}
	return json.Marshal(out)
}

func jsonObjectFields(value any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

type settingsHooksFile struct {
	values map[string]json.RawMessage
	hooks  map[string][]settingsHookGroup
}

func readSettingsHooks(path string) (settingsHooksFile, error) {
	settings := settingsHooksFile{
		values: map[string]json.RawMessage{},
		hooks:  map[string][]settingsHookGroup{},
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &settings.values); err != nil {
			return settingsHooksFile{}, err
		}
		if rawHooks, ok := settings.values["hooks"]; ok {
			if err := json.Unmarshal(rawHooks, &settings.hooks); err != nil {
				return settingsHooksFile{}, err
			}
		}
	} else if !os.IsNotExist(err) {
		return settingsHooksFile{}, err
	}
	if settings.hooks == nil {
		settings.hooks = map[string][]settingsHookGroup{}
	}
	return settings, nil
}

func (settings settingsHooksFile) marshal() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(settings.values)+1)
	for key, value := range settings.values {
		if key != "hooks" {
			out[key] = value
		}
	}
	if len(settings.hooks) > 0 {
		data, err := json.Marshal(settings.hooks)
		if err != nil {
			return nil, err
		}
		out["hooks"] = data
	}
	return json.MarshalIndent(out, "", "  ")
}

func installSettingsEndpointHooks(path, platform string, endpointHooks map[string]settingsHookGroup) error {
	settings, err := readSettingsHooks(path)
	if err != nil {
		return err
	}
	removeSettingsEndpointHooksFromLoaded(&settings, platform)
	for eventName, group := range endpointHooks {
		settings.hooks[eventName] = mergeSettingsEndpointHook(settings.hooks[eventName], group, platform)
	}
	data, err := settings.marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func removeSettingsEndpointHooksFromLoaded(settings *settingsHooksFile, platform string) bool {
	if settings == nil {
		return false
	}
	changed := false
	for eventName, groups := range settings.hooks {
		eventChanged := false
		filtered := groups[:0]
		for _, group := range groups {
			withoutEndpointHooks, groupChanged := filterSettingsEndpointHooks(group, platform)
			if groupChanged {
				eventChanged = true
			}
			// Drop a group only when removing Beacon's hooks emptied it. A group the user left
			// empty is theirs, and is kept like any other entry Beacon did not write.
			if groupChanged && len(withoutEndpointHooks.Hooks) == 0 {
				continue
			}
			filtered = append(filtered, withoutEndpointHooks)
		}
		if !eventChanged {
			continue
		}
		changed = true
		if len(filtered) == 0 {
			delete(settings.hooks, eventName)
		} else {
			settings.hooks[eventName] = filtered
		}
	}
	return changed
}

func mergeSettingsEndpointHook(existing []settingsHookGroup, group settingsHookGroup, platform string) []settingsHookGroup {
	out := make([]settingsHookGroup, 0, len(existing)+1)
	for _, item := range existing {
		filtered, changed := filterSettingsEndpointHooks(item, platform)
		if !changed || len(filtered.Hooks) > 0 {
			out = append(out, item)
			if changed {
				out[len(out)-1] = filtered
			}
		}
	}
	return append(out, group)
}

func removeSettingsEndpointHooks(path, platform string) (bool, error) {
	settings, err := readSettingsHooks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !removeSettingsEndpointHooksFromLoaded(&settings, platform) {
		return false, nil
	}
	out, err := settings.marshal()
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, out, 0600)
}

func filterSettingsEndpointHooks(group settingsHookGroup, platform string) (settingsHookGroup, bool) {
	filtered := group
	filtered.Hooks = group.Hooks[:0]
	changed := false
	for _, hook := range group.Hooks {
		if isSettingsEndpointHookCommand(hook.Command, platform) {
			changed = true
			continue
		}
		filtered.Hooks = append(filtered.Hooks, hook)
	}
	return filtered, changed
}

func isSettingsEndpointHookGroup(group settingsHookGroup, platform string) bool {
	for _, hook := range group.Hooks {
		if isSettingsEndpointHookCommand(hook.Command, platform) {
			return true
		}
	}
	return false
}

func isSettingsEndpointHookCommand(command, platform string) bool {
	if platform != "" && !strings.Contains(command, "--platform "+platform) {
		return false
	}
	return isEndpointHookCommand(command, platform)
}

func isSettingsEndpointInstalledAt(path, platform string) bool {
	settings, err := readSettingsHooks(path)
	if err != nil {
		return false
	}
	for _, groups := range settings.hooks {
		for _, group := range groups {
			if isSettingsEndpointHookGroup(group, platform) {
				return true
			}
		}
	}
	return false
}
