package config

import "testing"

func TestSettingSource(t *testing.T) {
	p := &Profile{Name: "default"}
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	for _, k := range Keys {
		if KeyDescriptions[k] == "" {
			t.Errorf("no description for %s", k)
		}
	}
	if v, src := SettingSource(p, "concurrency", get); v != "4" || src != SettingDefault {
		t.Fatalf("default: %q %q", v, src)
	}
	if err := SetKey(p, "concurrency", "8"); err != nil {
		t.Fatal(err)
	}
	if v, src := SettingSource(p, "concurrency", get); v != "8" || src != SettingFile {
		t.Fatalf("file: %q %q", v, src)
	}
	SetKey(p, "format", "csv")
	env["AUDD_FORMAT"] = "json"
	if v, src := SettingSource(p, "format", get); v != "json" || src != SettingEnv {
		t.Fatalf("env: %q %q", v, src)
	}
}
