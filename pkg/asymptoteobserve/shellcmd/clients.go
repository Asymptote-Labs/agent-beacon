package shellcmd

import (
	"slices"
	"strings"
)

// This file reads the words of curl and wget once, for what each sends (curlInputs, wgetInputs)
// and what it saves (curlOutputs, wgetOutputs): an option that takes a value takes it whatever
// fact asks, so a header or a proxy is never taken for a URL, nor a URL for a file to send.

// curlValueOptions are curl's short options that take a value.
const curlValueOptions = "AbcCdDeEFHKmoPQrtTuUwxXyYz"

// curlLongValues are curl's long options that take a value, when it is not attached with `=`.
var curlLongValues = []string{
	"--abstract-unix-socket", "--alt-svc", "--aws-sigv4", "--cacert", "--capath", "--cert", "--cert-type",
	"--ciphers", "--config", "--connect-timeout", "--connect-to", "--continue-at", "--cookie", "--cookie-jar",
	"--create-file-mode", "--curves", "--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode",
	"--delegation", "--dns-interface", "--dns-ipv4-addr", "--dns-ipv6-addr", "--dns-servers", "--doh-url",
	"--dump-header", "--ech", "--egd-file", "--engine", "--etag-compare", "--etag-save", "--expect100-timeout",
	"--form", "--form-string", "--ftp-account", "--ftp-alternative-to-user", "--ftp-method", "--ftp-port",
	"--ftp-ssl-ccc-mode", "--happy-eyeballs-timeout-ms", "--haproxy-clientip", "--header", "--hostpubmd5",
	"--hostpubsha256", "--hsts", "--interface", "--ip-tos", "--json", "--keepalive-time", "--key", "--key-type",
	"--krb", "--libcurl", "--limit-rate", "--local-port", "--login-options", "--mail-auth", "--mail-from",
	"--mail-rcpt", "--max-filesize", "--max-redirs", "--max-time", "--netrc-file", "--noproxy", "--oauth2-bearer",
	"--output", "--output-dir", "--parallel-max", "--pass", "--pinnedpubkey", "--preproxy", "--proto",
	"--proto-default", "--proto-redir", "--proxy", "--proxy-cacert", "--proxy-capath", "--proxy-cert",
	"--proxy-cert-type", "--proxy-ciphers", "--proxy-crlfile", "--proxy-header", "--proxy-key", "--proxy-key-type",
	"--proxy-pass", "--proxy-pinnedpubkey", "--proxy-service-name", "--proxy-tls13-ciphers", "--proxy-tlsauthtype",
	"--proxy-tlspassword", "--proxy-tlsuser", "--proxy-user", "--pubkey", "--quote", "--random-file", "--range",
	"--rate", "--referer", "--request", "--request-target", "--resolve", "--retry", "--retry-delay",
	"--retry-max-time", "--sasl-authzid", "--service-name", "--socks4", "--socks4a", "--socks5",
	"--socks5-gssapi-service", "--socks5-hostname", "--speed-limit", "--speed-time", "--stderr",
	"--telnet-option", "--tftp-blksize", "--time-cond", "--tls-max", "--tls13-ciphers", "--tlsauthtype",
	"--tlspassword", "--tlsuser", "--trace", "--trace-ascii", "--trace-config", "--unix-socket", "--upload-file",
	"--url", "--url-query", "--user", "--user-agent", "--variable", "--write-out",
}

// What the value of a curl option is to the request body.
const (
	curlData      = iota + 1 // the body, or @file
	curlURLEncode            // content, name=content, @file or name@file
	curlForm                 // name=content, name=@file or name=<file
	curlRaw                  // the body as written
	curlUpload               // a file
	curlCookie               // a cookie jar, or a literal cookie the word carries
	curlConfig               // a config file
	curlNetrc                // a netrc file
)

// curlArgs are the words of a curl command line its facts ask about.
type curlArgs struct {
	// sends are the values of the options that send a request body.
	sends []curlSend
	// reads are the values of the options that name a file curl reads to make the request:
	// the cookie jar of -b/--cookie, the config of -K/--config, and the netrc of
	// --netrc-file. A -b value that carries `=` is the literal cookie curl sends, which the
	// word's own value covers.
	reads []curlSend
	// outputs are the files of -o/--output, dir the value of --output-dir, remote whether
	// -O/--remote-name is given, and urls the operands and the values of --url.
	outputs, urls []Word
	dir           *Word
	remote        bool
}

// curlSend is the value of an option that sends a request body: what it is to the body (curlData
// and the others), and the index in the words of the one that holds it.
type curlSend struct {
	kind  int
	value Word
	index int
}

// readCurl reads curl's words: options, with their values, and URLs.
func readCurl(args []Word) curlArgs {
	var a curlArgs
	for i := 0; i < len(args); i++ {
		w := args[i]
		t := w.Unquoted
		if !isOptionText(t) {
			a.urls = append(a.urls, w)
			continue
		}
		var (
			name, rest string
			attached   bool
		)
		if t[1] == '-' {
			name, rest, attached = strings.Cut(t, "=")
			switch {
			case name == "--remote-name" || name == "--remote-name-all":
				a.remote = true
				continue
			case !slices.Contains(curlLongValues, name):
				continue
			}
		} else {
			j := valueLetter(t, w.Known, curlValueOptions)
			if strings.IndexByte(t[1:clusterEnd(t, w.Known, j)], 'O') >= 0 {
				a.remote = true
			}
			if j < 0 {
				continue // only options without a value
			}
			name, rest, attached = t[j:j+1], t[j+1:], j+1 < len(t)
		}
		value, last, ok := optionValue(args, i, rest, attached)
		if !ok {
			break
		}
		i = last
		switch name {
		case "o", "--output":
			a.outputs = append(a.outputs, value)
		case "--output-dir":
			a.dir = &value
		case "--url":
			a.urls = append(a.urls, value)
		case "d", "--data", "--data-binary", "--data-ascii", "--json":
			a.sends = append(a.sends, curlSend{curlData, value, last})
		case "--data-urlencode":
			a.sends = append(a.sends, curlSend{curlURLEncode, value, last})
		case "--data-raw", "--form-string":
			a.sends = append(a.sends, curlSend{curlRaw, value, last})
		case "F", "--form":
			a.sends = append(a.sends, curlSend{curlForm, value, last})
		case "T", "--upload-file":
			a.sends = append(a.sends, curlSend{curlUpload, value, last})
		case "b", "--cookie":
			a.reads = append(a.reads, curlSend{curlCookie, value, last})
		case "K", "--config":
			a.reads = append(a.reads, curlSend{curlConfig, value, last})
		case "--netrc-file":
			a.reads = append(a.reads, curlSend{curlNetrc, value, last})
		}
	}
	return a
}

// optionValue is the value of an option written in the word at i: what follows the option in
// that word when attached, and otherwise the next word. It returns the index of the last word
// read, and ok false when the value is missing.
func optionValue(args []Word, i int, rest string, attached bool) (Word, int, bool) {
	switch {
	case attached:
		return attachedValue(args[i], rest), i, true
	case i+1 < len(args):
		return args[i+1], i + 1, true
	}
	return Word{}, i, false
}

// wgetValueOptions are wget's short options that take a value.
const wgetValueOptions = "aoOiBtTwQPUelARDIX"

// wgetLongValues are wget's long options that take a value, when it is not attached with `=`.
var wgetLongValues = []string{
	"--accept", "--accept-regex", "--append-output", "--base", "--bind-address", "--body-data", "--body-file",
	"--ca-certificate", "--ca-directory", "--certificate", "--certificate-type", "--ciphers", "--config",
	"--connect-timeout", "--crl-file", "--cut-dirs", "--default-page", "--directory-prefix", "--dns-timeout",
	"--domains", "--exclude-directories", "--exclude-domains", "--execute", "--ftp-password", "--ftp-user",
	"--header", "--hsts-file", "--http-password", "--http-user", "--include-directories", "--input-file",
	"--level", "--limit-rate", "--load-cookies", "--local-encoding", "--max-redirect", "--method",
	"--output-document", "--output-file", "--password", "--post-data", "--post-file", "--private-key",
	"--private-key-type", "--progress", "--proxy-password", "--proxy-user", "--quota", "--read-timeout",
	"--referer", "--regex-type", "--reject", "--reject-regex", "--remote-encoding", "--report-speed",
	"--restrict-file-names", "--save-cookies", "--secure-protocol", "--timeout", "--tries", "--use-askpass",
	"--user", "--user-agent", "--wait", "--waitretry", "--warc-file",
}

// wgetArgs are the words of a wget command line its facts ask about.
type wgetArgs struct {
	// posts are the values of --post-file and --body-file (file) and --post-data and
	// --body-data, with the index of the word that holds each.
	posts []wgetPost
	// output is the value of -O/--output-document, dir that of -P/--directory-prefix, urls the
	// operands, and named whether the download is saved under the names its URLs end in: not
	// for a recursive download (-r, -m, -p) or --spider.
	output, dir *Word
	urls        []Word
	named       bool
}

type wgetPost struct {
	file  bool
	value Word
	index int
}

// readWget reads wget's words: options, with their values, and URLs.
func readWget(args []Word) wgetArgs {
	a := wgetArgs{named: true}
	for i := 0; i < len(args); i++ {
		w := args[i]
		t := w.Unquoted
		if !isOptionText(t) {
			a.urls = append(a.urls, w)
			continue
		}
		var (
			name, rest string
			attached   bool
		)
		if t[1] == '-' {
			name, rest, attached = strings.Cut(t, "=")
			switch {
			case name == "--recursive" || name == "--mirror" || name == "--spider" || name == "--page-requisites":
				a.named = false
				continue
			case !slices.Contains(wgetLongValues, name):
				continue
			}
		} else {
			j := 1
			for j < len(t) && !(!w.Known && (t[j] == '$' || t[j] == '`')) && strings.IndexByte(wgetValueOptions, t[j]) < 0 {
				if t[j] == 'n' {
					j += 2 // -nv, -nc, -np, -nH: one option of two letters
					continue
				}
				a.named = a.named && t[j] != 'r' && t[j] != 'm' && t[j] != 'p'
				j++
			}
			if j >= len(t) || strings.IndexByte(wgetValueOptions, t[j]) < 0 {
				continue // only options without a value
			}
			name, rest, attached = t[j:j+1], t[j+1:], j+1 < len(t)
		}
		value, last, ok := optionValue(args, i, rest, attached)
		if !ok {
			break
		}
		i = last
		switch name {
		case "O", "--output-document":
			a.output = &value
		case "P", "--directory-prefix":
			a.dir = &value
		case "--post-file", "--body-file":
			a.posts = append(a.posts, wgetPost{true, value, last})
		case "--post-data", "--body-data":
			a.posts = append(a.posts, wgetPost{false, value, last})
		}
	}
	return a
}
