package asymptoteobserve

import "net/url"

// FilePathFromURI returns the path a file: URI names, and "" for anything that is not one. A
// host other than localhost is a UNC share (//host/path). The slash before a Windows drive letter
// is URI syntax, so file:///C:/Users/x names C:/Users/x. Percent-escapes are decoded.
func FilePathFromURI(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return ""
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "//" + parsed.Host + parsed.Path
	}
	if p := parsed.Path; len(p) >= 3 && p[0] == '/' && p[2] == ':' && (p[1]|0x20 >= 'a' && p[1]|0x20 <= 'z') {
		return p[1:]
	}
	return parsed.Path
}
