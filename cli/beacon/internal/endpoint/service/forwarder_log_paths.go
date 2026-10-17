package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

// LogPathsCurrent reports whether this launchd unit uses the current log paths.
// Other unit settings are deliberately not compared: an operator's arguments or
// formatting must not cause a current unit to be replaced during an upgrade.
func (m ForwarderManager) LogPathsCurrent() (bool, error) {
	if m.resolvedKind() != KindLaunchd {
		return true, nil
	}
	path, err := m.UnitPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	stdout, stderr, err := launchdLogPaths(m.UserMode, ForwarderLabel)
	if err != nil {
		return false, err
	}
	return forwarderLogPathsCurrent(data, stdout, stderr)
}

func forwarderLogPathsCurrent(data []byte, stdout, stderr string) (bool, error) {
	type entry struct {
		XMLName  xml.Name
		Value    string  `xml:",chardata"`
		Children []entry `xml:",any"`
	}
	var plist struct {
		XMLName xml.Name `xml:"plist"`
		Dicts   []struct {
			Entries []entry `xml:",any"`
		} `xml:"dict"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	// Decode scans past character data before a root; require the first actual
	// element to be the plist, without accepting a corrupt text prefix.
	var root xml.StartElement
findRoot:
	for {
		token, err := decoder.Token()
		if err != nil {
			return false, fmt.Errorf("read forwarder plist: %w", err)
		}
		switch token := token.(type) {
		case xml.CharData:
			if len(bytes.Trim(token, " \t\r\n")) != 0 {
				return false, fmt.Errorf("text before forwarder plist")
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
			// XML declarations, comments and the plist DOCTYPE precede the root.
		case xml.StartElement:
			if token.Name.Local != "plist" {
				return false, fmt.Errorf("forwarder unit root must be plist")
			}
			root = token
			break findRoot
		default:
			return false, fmt.Errorf("unexpected content before forwarder plist")
		}
	}
	if err := decoder.DecodeElement(&plist, &root); err != nil {
		return false, fmt.Errorf("read forwarder plist: %w", err)
	}
	// Decode alone stops at the first root. Refuse a second root or text rather
	// than treating a partially parsed file as a current launchd unit.
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, fmt.Errorf("read forwarder plist: %w", err)
		}
		switch token := token.(type) {
		case xml.CharData:
			if len(bytes.Trim(token, " \t\r\n")) != 0 {
				return false, fmt.Errorf("text after forwarder plist")
			}
		case xml.Comment:
		case xml.ProcInst:
			if strings.EqualFold(token.Target, "xml") {
				return false, fmt.Errorf("XML declaration after forwarder plist")
			}
		default:
			return false, fmt.Errorf("unexpected content after forwarder plist")
		}
	}
	if len(plist.Dicts) != 1 {
		return false, fmt.Errorf("forwarder plist must contain one dictionary")
	}
	entries := plist.Dicts[0].Entries
	paths := make(map[string]string, 2)
	for i := 0; i < len(entries); i += 2 {
		key := entries[i]
		if key.XMLName.Local != "key" || key.XMLName.Space != "" || len(key.Children) != 0 || i+1 >= len(entries) {
			return false, fmt.Errorf("invalid forwarder plist dictionary entry")
		}
		if key.Value != "StandardOutPath" && key.Value != "StandardErrorPath" {
			continue
		}
		if _, duplicate := paths[key.Value]; duplicate {
			return false, fmt.Errorf("duplicate forwarder plist %s", key.Value)
		}
		value := entries[i+1]
		if value.XMLName.Local != "string" || value.XMLName.Space != "" || len(value.Children) != 0 {
			return false, fmt.Errorf("forwarder plist %s must be a string", key.Value)
		}
		paths[key.Value] = value.Value
	}
	return paths["StandardOutPath"] == stdout && paths["StandardErrorPath"] == stderr, nil
}
