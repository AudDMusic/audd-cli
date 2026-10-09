package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

const keysHelp = `Keys:
  token                        API token, kept in the system credential store
                               (audd config set token - reads it from stdin)
  format                       default output format: table, json, jsonl, csv
  max_requests                 default --max-requests for every command
  concurrency                  parallel requests for batch recognition (default 4)
  streams.background_recorder  start the background stream recorder automatically (default true)

Settings apply to the active profile; pass --profile to change another one.`

func newConfigCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config",
		Short:   "Read and change settings",
		Long:    "Read and change audd settings.\n\n" + keysHelp,
		GroupID: GroupLocal,
		Example: `  audd config set token your-api-token
  audd config set token - < token.txt
  audd config set format json
  audd config set streams.background_recorder false
  audd config list`,
	}
	keyArg := func(n int) cobra.PositionalArgs {
		return cobra.ExactArgs(n)
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:       "get <key>",
			Short:     "Show one setting",
			Args:      keyArg(1),
			ValidArgs: config.Keys,
			RunE: func(cmd *cobra.Command, args []string) error {
				v, ok, err := getSetting(a, args[0])
				if err != nil {
					return err
				}
				return a.Out.Result(settingView{Key: args[0], Value: nullable(v, ok), Profile: a.Profile.Name}, func(w io.Writer) {
					if ok {
						fmt.Fprintln(w, v)
					} else {
						a.Out.Info("%s is not set", args[0])
					}
				})
			},
		},
		&cobra.Command{
			Use:       "set <key> <value>",
			Short:     "Change a setting",
			Long:      "Change a setting.\n\n" + keysHelp,
			Args:      keyArg(2),
			ValidArgs: config.Keys,
			RunE: func(cmd *cobra.Command, args []string) error {
				key, value := args[0], args[1]
				if key == "token" {
					if value == "-" {
						// Keeps the token out of shell history and process listings.
						b, err := io.ReadAll(io.LimitReader(a.In, 64<<10))
						if err != nil {
							return output.AsError(err)
						}
						value = string(b)
					}
					value = strings.TrimSpace(value)
					if value == "" {
						return output.Errf(output.ExitUsage, "invalid_argument", "", "the token is empty")
					}
					if err := a.Secrets.Set(a.Profile.Name, "api_token", value); err != nil {
						return err
					}
					restartRecorder(a)
					if os.Getenv("AUDD_API_TOKEN") != "" {
						a.Out.Warn("Note: AUDD_API_TOKEN is set and takes precedence over this token.")
					}
				} else {
					if err := config.SetKey(a.Profile, key, value); err != nil {
						return err
					}
					if err := a.Cfg.Save(); err != nil {
						return err
					}
				}
				shown, _, _ := getSetting(a, key)
				return a.Out.Result(settingView{Key: key, Value: &shown, Profile: a.Profile.Name}, func(w io.Writer) {
					a.Out.Info("Set %s = %s (profile %s).", key, shown, a.Profile.Name)
				})
			},
		},
		&cobra.Command{
			Use:       "unset <key>",
			Short:     "Remove a setting",
			Args:      keyArg(1),
			ValidArgs: config.Keys,
			RunE: func(cmd *cobra.Command, args []string) error {
				key := args[0]
				if key == "token" {
					if err := a.Secrets.Delete(a.Profile.Name, "api_token"); err != nil {
						return err
					}
					restartRecorder(a)
				} else {
					if err := config.UnsetKey(a.Profile, key); err != nil {
						return err
					}
					if err := a.Cfg.Save(); err != nil {
						return err
					}
				}
				return a.Out.Result(settingView{Key: key, Profile: a.Profile.Name}, func(w io.Writer) {
					a.Out.Info("Removed %s (profile %s).", key, a.Profile.Name)
				})
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "Show all settings",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				view := configList{Profile: a.Profile.Name, Path: a.Cfg.Path, Settings: newOrderedSettings()}
				for _, k := range config.Keys {
					v, ok, err := getSetting(a, k)
					if err != nil {
						return err
					}
					view.Settings.set(k, nullable(v, ok))
				}
				return a.Out.Result(view, func(w io.Writer) {
					tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
					for _, k := range config.Keys {
						v := view.Settings.m[k]
						s := "(not set)"
						if v != nil {
							s = *v
						}
						fmt.Fprintf(tw, "%s\t%s\n", k, s)
					}
					tw.Flush()
					a.Out.Info("Profile %s, %s", a.Profile.Name, a.Cfg.Path)
				})
			},
		},
	)
	return cmd
}

type settingView struct {
	Key     string  `json:"key"`
	Value   *string `json:"value"`
	Profile string  `json:"profile"`
}

type configList struct {
	Profile  string          `json:"profile"`
	Path     string          `json:"path"`
	Settings orderedSettings `json:"settings"`
}

// orderedSettings marshals settings in config.Keys order.
type orderedSettings struct {
	keys []string
	m    map[string]*string
}

func newOrderedSettings() orderedSettings { return orderedSettings{m: map[string]*string{}} }

func (o *orderedSettings) set(k string, v *string) {
	o.keys = append(o.keys, k)
	o.m[k] = v
}

func (o orderedSettings) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(o.m[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func nullable(v string, ok bool) *string {
	if !ok {
		return nil
	}
	return &v
}

// getSetting returns a setting for display. The token is always masked;
// use `audd token show --reveal` to see it.
func getSetting(a *app.App, key string) (string, bool, error) {
	if key == "token" {
		t, err := a.Secrets.Get(a.Profile.Name, "api_token")
		if errors.Is(err, secrets.ErrNotFound) || (err == nil && t == "") {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return config.MaskToken(t), true, nil
	}
	for _, k := range config.Keys {
		if k == key {
			v, ok := config.GetKey(a.Profile, key)
			return v, ok, nil
		}
	}
	return "", false, output.Errf(output.ExitUsage, "invalid_argument", "keys: "+strings.Join(config.Keys, ", "), "unknown config key %q", key)
}
