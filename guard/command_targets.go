package guard

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/Autumn-27/artex/db"
)

// Only command-bearing fields contain shell syntax. Other fields are handled
// by their declared host/URL semantics, never by substring domain matching.
func collectCommandHosts(value any, add func(string)) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if text, ok := child.(string); ok && (key == "command" || key == "text" || key == "script") {
				collectShellTargets(text, add)
			} else {
				collectCommandHosts(child, add)
			}
		}
	case []any:
		for _, child := range v {
			collectCommandHosts(child, add)
		}
	}
}

// This lexer preserves whole arguments (including underscores and paths) and
// shell command boundaries. It does not evaluate variables or execute scripts.
func shellTargetTokens(command string) []string {
	var tokens []string
	var word strings.Builder
	var quote rune
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case strings.ContainsRune(";|&\n", r):
			flush()
			tokens = append(tokens, ";")
		case r == '>' || r == '<':
			flush()
			tokens = append(tokens, ">")
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func collectShellTargets(command string, add func(string)) {
	var executable string
	skip := false
	for _, token := range shellTargetTokens(command) {
		if token == ";" {
			executable, skip = "", false
			continue
		}
		if token == ">" {
			skip = true
			continue
		}
		if skip {
			skip = false
			continue
		}
		if executable == "" {
			name := strings.ReplaceAll(token, "\\", "/")
			name = name[strings.LastIndex(name, "/")+1:]
			executable = strings.TrimSuffix(strings.ToLower(name), ".exe")
			continue
		}
		switch executable {
		case "curl", "wget", "ping", "ping6", "nslookup", "dig", "host", "nmap", "nc", "netcat", "traceroute", "tracert":
		default:
			continue
		}
		// Values of these options are local files, headers, credentials, ports,
		// or timing parameters. Never reinterpret their contents as a host.
		if executable == "nmap" && token == "-A" {
			continue // Aggressive scan is a flag, unlike curl's user-agent option.
		}
		switch token {
		case "-o", "-D", "-H", "-A", "-e", "-u", "-U", "-d", "-F", "-T", "-X", "-m", "-p", "-iL", "-oN", "-oX", "-oG", "-oA", "-x",
			"--output", "--dump-header", "--header", "--data", "--data-raw", "--data-binary", "--form", "--upload-file", "--user", "--proxy-user", "--max-time", "--connect-timeout", "--cacert", "--cert", "--key", "--proxy":
			skip = true
			continue
		}
		if strings.HasPrefix(token, "-") {
			if strings.HasPrefix(token, "--url=") {
				token = strings.TrimPrefix(token, "--url=")
			} else {
				continue
			}
		}
		if strings.Contains(token, "://") {
			if u, err := url.Parse(token); err == nil && u.Hostname() != "" {
				add(u.Hostname())
			}
			continue
		}
		if (executable == "curl" || executable == "wget") && !strings.HasPrefix(token, "/") && strings.Contains(token, "/") && !strings.ContainsAny(token, `\\$={}()`) {
			if u, err := url.Parse("//" + token); err == nil {
				if host, err := db.NormalizeAgentHost(u.Hostname(), true); err == nil {
					add(host)
				}
			}
			continue
		}
		if strings.ContainsAny(token, `/\\$={}()`) {
			continue
		}
		if _, err := strconv.Atoi(token); err == nil {
			continue
		}
		if (executable == "dig" || executable == "host" || executable == "nslookup") &&
			strings.Contains("|A|AAAA|CNAME|MX|TXT|SRV|NS|SOA|ANY|", "|"+token+"|") {
			continue
		}
		u, err := url.Parse("//" + token)
		if err == nil {
			if host, err := db.NormalizeAgentHost(u.Hostname(), true); err == nil {
				add(host)
			}
		}
	}
}
