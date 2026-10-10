package cli

import (
	"fmt"
	"io"

	"github.com/AudDMusic/audd-go"
	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/safety"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newCatalogCmd(a))
	})
}

type catalogAdded struct {
	AudioID int    `json:"audio_id"`
	Input   string `json:"input"`
	Added   bool   `json:"added"`
}

func newCatalogCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "catalog",
		Short:   "Add songs to your custom catalog",
		GroupID: GroupRecognize,
		Long: `Your custom catalog is a private set of songs that recognition also matches
against. A match from it carries the audio_id you chose instead of artist and
title. Keep your own list of which song is under which ID: the API has no
way to list or delete them.

Custom catalog access is enabled per account; write to api@audd.io for it.`,
	}
	var id int
	add := &cobra.Command{
		Use:   "add <file|url> --id N",
		Short: "Fingerprint a song into your custom catalog",
		Long: `Fingerprint a song into your custom catalog under audio_id N.

If N already holds a song, it is replaced. Each upload is billed, so it is
sent exactly once and never retried automatically; if it fails, check the
error before running it again.`,
		Example: `  audd catalog add song.mp3 --id 42
  audd catalog add https://example.com/song.mp3 --id 43 --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if id < 0 {
				return usageErr("use --id N with N of 0 or more", "invalid --id %d", id)
			}
			inputs, batch, err := media.ExpandInputs(args, a.In)
			if err != nil {
				return err
			}
			defer func() {
				for _, in := range inputs {
					in.Cleanup()
				}
			}()
			if batch {
				return usageErr("add songs one at a time: audd catalog add <file|url> --id N", "catalog add takes one file or URL")
			}
			in := inputs[0]
			if err := requireToken(a); err != nil {
				return err
			}
			if a.Out.CanAsk() {
				if err := a.Out.Confirm(fmt.Sprintf("Add %s to your custom catalog as audio_id %d? A song already under that ID is replaced.", in.Name(), id), a.Flags.Yes); err != nil {
					return err
				}
			}
			if err := safety.BudgetFor(a).Take(1); err != nil {
				return err
			}
			src := in.URL
			if src == "" {
				src = in.Path
			}
			if _, err := api.Do(ctx, a, func(c *audd.Client) (struct{}, error) {
				return struct{}{}, c.CustomCatalog().AddContext(ctx, id, src)
			}); err != nil {
				return err
			}
			return a.Out.Result(catalogAdded{AudioID: id, Input: in.Name(), Added: true}, func(w io.Writer) {
				fmt.Fprintf(w, "Added %s to your custom catalog as audio_id %d.\n", in.Name(), id)
				if !a.Out.Quiet() {
					fmt.Fprintf(w, "Recognition returns audio_id %d when it matches this song.\n", id)
				}
			})
		},
	}
	add.Flags().IntVar(&id, "id", 0, "audio_id to store the song under (required)")
	_ = add.MarkFlagRequired("id")
	cmd.AddCommand(add)
	return cmd
}
