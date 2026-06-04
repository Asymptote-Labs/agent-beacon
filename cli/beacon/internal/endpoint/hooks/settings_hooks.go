package hooks

import (
	"encoding/json"
	"os"
	"strings"
)

type settingsHookGroup struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []settingsHookRef `json:"hooks"`
}

type settingsHookRef struct {
	Type       string `json:"type,omitempty"`
	Command    string `json:"command"`
	Timeout    int    `json:"timeout,omitempty"`
	ShowOutput *bool  `json:"show_output,omitempty"`
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
	for eventName, group := range endpointHooks {
		settings.hooks[eventName] = mergeSettingsEndpointHook(settings.hooks[eventName], group, platform)
	}
	data, err := settings.marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
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
	changed := false
	for eventName, groups := range settings.hooks {
		filtered := groups[:0]
		for _, group := range groups {
			withoutEndpointHooks, groupChanged := filterSettingsEndpointHooks(group, platform)
			if groupChanged {
				changed = true
			}
			if len(withoutEndpointHooks.Hooks) == 0 {
				continue
			}
			filtered = append(filtered, withoutEndpointHooks)
		}
		if len(filtered) == 0 {
			delete(settings.hooks, eventName)
		} else {
			settings.hooks[eventName] = filtered
		}
	}
	if !changed {
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
