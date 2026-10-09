package output

import "strings"

// Format is an output format.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatJSONL Format = "jsonl"
	FormatCSV   Format = "csv"
)

// Formats lists the accepted --format values.
var Formats = []Format{FormatTable, FormatJSON, FormatJSONL, FormatCSV}

// ParseFormat validates a --format / AUDD_FORMAT value.
func ParseFormat(s string) (Format, error) {
	f := Format(strings.ToLower(strings.TrimSpace(s)))
	for _, ok := range Formats {
		if f == ok {
			return f, nil
		}
	}
	return "", Errf(ExitUsage, "invalid_argument", "use --format table|json|jsonl|csv", "unknown output format %q", s)
}

// ResolveFormat applies the precedence flag > env (AUDD_FORMAT or a config
// default) > automatic. Automatic is table on a TTY, json when piped, and
// jsonl when piped for streaming commands. explicit reports whether the
// format came from the flag or env rather than automatic detection.
func ResolveFormat(flag, env string, stdoutTTY, streaming bool) (f Format, explicit bool, err error) {
	for _, v := range []string{flag, env} {
		if strings.TrimSpace(v) != "" {
			f, err := ParseFormat(v)
			return f, err == nil, err
		}
	}
	return autoFormat(stdoutTTY, streaming), false, nil
}

func autoFormat(stdoutTTY, streaming bool) Format {
	switch {
	case stdoutTTY:
		return FormatTable
	case streaming:
		return FormatJSONL
	default:
		return FormatJSON
	}
}

// ResolveNoColor decides whether color is disabled: --no-color or NO_COLOR
// disable it, FORCE_COLOR forces it on, otherwise color follows the TTY.
func ResolveNoColor(flag bool, getenv func(string) string, stdoutTTY bool) bool {
	if flag {
		return true
	}
	if v := getenv("FORCE_COLOR"); v != "" && v != "0" && v != "false" {
		return false
	}
	if getenv("NO_COLOR") != "" {
		return true
	}
	return !stdoutTTY
}
