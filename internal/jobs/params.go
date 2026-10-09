package jobs

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/media"
)

// Params are the recognition settings of a job. They are stored with the
// job, so `audd jobs resume <id>` needs nothing but the ID.
type Params struct {
	Enterprise bool
	Return     string            // extra metadata (standard endpoint only)
	Limit      *int              // enterprise chunks per file; nil = no limit
	Options    map[string]string // enterprise passthrough: skip, every, skip_first_seconds, use_timecode, accurate_offsets
}

func paramsFromOptions(o app.BatchOptions) Params {
	p := Params{Enterprise: o.Enterprise, Limit: o.Limit}
	if !o.Enterprise {
		p.Return = o.Return
		return p
	}
	if len(o.EnterpriseOpts) > 0 {
		p.Options = map[string]string{}
		for k, v := range o.EnterpriseOpts {
			p.Options[k] = v
		}
	}
	return p
}

// Endpoint is "standard" or "enterprise".
func (p Params) Endpoint() string {
	if p.Enterprise {
		return "enterprise"
	}
	return "standard"
}

// Map is the stored form: endpoint, return, limit ("none" or a number),
// and "opt.<name>" for each passthrough option.
func (p Params) Map() map[string]string {
	m := map[string]string{"endpoint": p.Endpoint()}
	if p.Return != "" {
		m["return"] = p.Return
	}
	if p.Enterprise {
		m["limit"] = "none"
		if p.Limit != nil {
			m["limit"] = strconv.Itoa(*p.Limit)
		}
	}
	for k, v := range p.Options {
		m["opt."+k] = v
	}
	return m
}

// ParamsFromMap is the inverse of Map.
func ParamsFromMap(m map[string]string) Params {
	p := Params{Enterprise: m["endpoint"] == "enterprise", Return: m["return"]}
	if n, err := strconv.Atoi(m["limit"]); err == nil {
		p.Limit = &n
	}
	for k, v := range m {
		if name, ok := strings.CutPrefix(k, "opt."); ok {
			if p.Options == nil {
				p.Options = map[string]string{}
			}
			p.Options[name] = v
		}
	}
	return p
}

// optInt reads an integer passthrough option.
func (p Params) optInt(name string) (int, bool) {
	n, err := strconv.Atoi(p.Options[name])
	return n, err == nil
}

// optIntPtr is optInt as a pointer, nil when the option is not set.
func (p Params) optIntPtr(name string) *int {
	if n, ok := p.optInt(name); ok {
		return &n
	}
	return nil
}

// commandKey identifies a batch by what it recognizes: the sorted inputs
// (absolute paths and URLs). Together with Params it decides whether a new
// run repeats an earlier job.
func commandKey(inputs []media.Input) []string {
	keys := make([]string, 0, len(inputs))
	for _, in := range inputs {
		switch {
		case in.URL != "":
			keys = append(keys, in.URL)
		case in.IsStdin:
			keys = append(keys, "stdin:"+in.Path)
		default:
			p := in.Path
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			keys = append(keys, p)
		}
	}
	sort.Strings(keys)
	return append([]string{"recognize"}, keys...)
}
