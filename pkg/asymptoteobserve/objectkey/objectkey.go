// Package objectkey builds and escapes the S3 and GCS object names Beacon uploads runtime logs
// under: the CI uploader, the cloud-agent hook shuttle and the Devin cloud puller.
package objectkey

import (
	"net/url"
	"strings"
)

// CleanParts splits a slash-separated value such as a configured prefix or an owner/repo
// name into cleaned key segments, dropping segments that clean to nothing.
func CleanParts(value string) []string {
	var parts []string
	for _, part := range strings.Split(strings.Trim(value, "/"), "/") {
		if cleaned := CleanPart(part); cleaned != "" {
			parts = append(parts, cleaned)
		}
	}
	return parts
}

// CleanPart makes one value safe to use as a single key segment: it trims spaces, turns
// backslashes into dashes, strips surrounding slashes, and rejects "." and ".." so a value
// cannot climb out of its prefix. It returns "" when nothing usable is left.
func CleanPart(value string) string {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.ReplaceAll(cleaned, "\\", "-")
	cleaned = strings.Trim(cleaned, "/")
	if cleaned == "." || cleaned == ".." {
		return ""
	}
	return cleaned
}

// EscapeName percent-encodes an object name (or a bucket name) for a storage URL path,
// segment by segment, keeping its slashes as separators.
func EscapeName(name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = (&url.URL{Path: part}).EscapedPath()
	}
	return strings.Join(parts, "/")
}
