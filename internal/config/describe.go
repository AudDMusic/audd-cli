package config

import "strings"

// KeyDescriptions say what each setting in Keys does.
var KeyDescriptions = map[string]string{
	"token":                       "API token, kept in the system credential store",
	"format":                      "default output format: table, json, jsonl, csv (auto: table on a terminal, JSON when piped)",
	"max_requests":                "default --max-requests for every command",
	"concurrency":                 "parallel requests for batch recognition",
	"streams.background_recorder": "start the background stream recorder automatically",
}

// KeyDefaults are the values used when a setting is not set.
var KeyDefaults = map[string]string{
	"format":                      "auto",
	"max_requests":                "no limit",
	"concurrency":                 "4",
	"streams.background_recorder": "true",
}

// Setting sources.
const (
	SettingDefault = "default"
	SettingFile    = "file"
	SettingEnv     = "env"
)

// SettingSource returns the value in effect for a setting other than the
// token, and where it comes from: the environment (AUDD_FORMAT), the
// config file, or the default.
func SettingSource(p *Profile, key string, getenv func(string) string) (value, source string) {
	if key == "format" && getenv != nil {
		if v := strings.TrimSpace(getenv("AUDD_FORMAT")); v != "" {
			return v, SettingEnv
		}
	}
	if v, ok := GetKey(p, key); ok {
		return v, SettingFile
	}
	return KeyDefaults[key], SettingDefault
}
