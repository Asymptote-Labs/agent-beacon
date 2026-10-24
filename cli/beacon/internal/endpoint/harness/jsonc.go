package harness

// standardizeJSONC turns a JSONC document (VS Code's settings.json allows // and /* */ comments and
// trailing commas) into plain JSON for encoding/json. Comments become spaces, so error offsets
// still point at the original bytes, and a comma followed only by whitespace before a closing
// bracket is dropped. Text inside strings is never touched.
func standardizeJSONC(data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	inString := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			for ; i < len(out) && out[i] != '\n'; i++ {
				out[i] = ' '
			}
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			out[i], out[i+1] = ' ', ' '
			for i += 2; i < len(out); i++ {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					out[i], out[i+1] = ' ', ' '
					i++
					break
				}
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
		}
	}
	inString = false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c != ',' {
			continue
		}
		j := i + 1
		for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
			j++
		}
		if j < len(out) && (out[j] == '}' || out[j] == ']') {
			out[i] = ' '
		}
	}
	return out
}
