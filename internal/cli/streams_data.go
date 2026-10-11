package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// storeFresh is how recently the store must have been brought up to date
// (by a recorder heartbeat or a recent-results fetch) for history and
// report to skip fetching recent results again.
const storeFresh = 2 * time.Minute

var relSince = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(s|m|min|h|d|w)$`)

// parseSince reads --since: a duration back from now ("90m", "24h", "7d",
// "2w"), a date ("2026-10-01", local time), an RFC 3339 time, or "all".
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "all" {
		return time.Time{}, nil
	}
	if m := relSince.FindStringSubmatch(s); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "min": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
		return now.Add(-time.Duration(n * float64(unit))), nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02t15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, strings.ToUpper(s)); err == nil {
		return t, nil
	}
	return time.Time{}, output.Errf(output.ExitUsage, "invalid_argument", `use a duration like 24h, 7d, or 2w, a date like 2026-10-01, or "all"`,
		"cannot read --since %q", s)
}

// refreshStore fills the store from the recent-results endpoint for the
// given streams (none: every stream on the account) that are stale: no
// recorder heartbeat or fetch for them in the last two minutes. Streams a
// running recorder keeps current are not fetched again. Failures only
// produce a note.
func refreshStore(ctx context.Context, a *app.App, st *streamstore.Store, ids []int) {
	check := ids
	if len(check) == 0 {
		check, _ = st.AccountStreams()
	}
	if len(check) > 0 {
		if last, err := st.LastHeartbeat(check...); err == nil && !last.IsZero() && a.Now().Sub(last) < storeFresh {
			return
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := streams.Backfill(ctx, a, st, ids, storeFresh); err != nil {
		e := output.AsError(err)
		if e.Code != "not_implemented" && e.Code != "no_token" {
			a.Out.Info("Could not fetch recent results: %s", e.Message)
		}
	}
}

// noStreamsHint is shown instead of gaps when there are no streams at all.
const noStreamsHint = "No streams yet. Add one with: audd streams add <url> --id 1"

// streamGaps is the store's gaps for the range, or none (and noStreams set)
// when there are no streams: with nothing to record, nothing is missing.
func streamGaps(st *streamstore.Store, from time.Time, ids []int) (gaps []streamstore.Gap, noStreams bool, err error) {
	if len(ids) == 0 {
		has, err := st.HasStreams()
		if err != nil {
			return nil, false, err
		}
		if !has {
			return []streamstore.Gap{}, true, nil
		}
	}
	gaps, err = st.Gaps(from, ids...)
	return gaps, false, err
}

// gapNote says which periods of a range were not recorded: "Some
// periods weren't recorded: Oct 9 13:00 – 14:30, Oct 10 02:00 – 03:10".
func gapNote(gaps []streamstore.Gap) string {
	var parts []string
	for i, g := range gaps {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(gaps)-3))
			break
		}
		parts = append(parts, gapRange(g))
	}
	return "Some periods weren't recorded: " + strings.Join(parts, ", ")
}

// gapRange is a gap in short: "Oct 9 13:00 – 14:30" (the date again when
// it ends on another day).
func gapRange(g streamstore.Gap) string {
	from, to := g.From.Local(), g.To.Local()
	end := to.Format("15:04")
	if from.YearDay() != to.YearDay() || from.Year() != to.Year() {
		end = to.Format("Jan 2 15:04")
	}
	// No break inside a range when a note wraps.
	return strings.ReplaceAll(from.Format("Jan 2 15:04")+" – "+end, " ", "\u00a0")
}

func fmtLength(sec int) string {
	if sec <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h >= 48:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func gapLine(g streamstore.Gap) string {
	// Short, so both times fit at 80 columns: "not recorded Oct 9 13:00 –
	// 14:30 (1h 30m)".
	return fmt.Sprintf("not recorded %s (%s)", strings.ReplaceAll(gapRange(g), "\u00a0", " "), fmtDuration(g.To.Sub(g.From)))
}

type historyDoc struct {
	Since *time.Time         `json:"since"`
	Plays []streamstore.Play `json:"plays"`
	Gaps  []streamstore.Gap  `json:"gaps"`
}

func newStreamsHistoryCmd(a *app.App) *cobra.Command {
	var id int
	var since string
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recorded plays, newest first",
		Long: `Show plays from the local stream store, newest first. For a stream no
running recorder keeps current, audd first fetches its recent results (the
last ~30), so the list is up to date.

Periods when a stream was not recorded (no recorder ran for it, or the
recorder could not reach AudD for longer than the recent results cover) are
listed as gaps ("not recorded"), so you can see where plays may be missing.
Without --id, a period missing for any stream on the account is a gap.`,
		Example: `  audd streams history
  audd streams history --id 1 --since 24h
  audd streams history --since 2026-10-01 --limit 0 --format csv > plays.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			now := a.Now()
			from, err := parseSince(since, now)
			if err != nil {
				return err
			}
			var idp *int
			var ids []int
			if cmd.Flags().Changed("id") {
				idp, ids = &id, []int{id}
			}
			st, err := openStore(a)
			if err != nil {
				return err
			}
			defer st.Close()
			refreshStore(cmd.Context(), a, st, ids)
			plays, err := st.History(idp, from, limit)
			if err != nil {
				return err
			}
			gaps, noStreams, err := streamGaps(st, from, ids)
			if err != nil {
				return err
			}
			ensureRecorder(a)
			if noStreams && len(plays) == 0 && !a.Out.IsHuman() {
				a.Out.Info("%s", noStreamsHint)
			}
			return printHistory(a, from, plays, gaps, noStreams)
		},
	}
	cmd.Flags().IntVar(&id, "id", 0, "only this stream")
	cmd.Flags().StringVar(&since, "since", "7d", `how far back: 24h, 7d, 2w, a date, or "all"`)
	cmd.Flags().IntVar(&limit, "limit", 50, "at most this many plays (0: all)")
	return cmd
}

func printHistory(a *app.App, from time.Time, plays []streamstore.Play, gaps []streamstore.Gap, noStreams bool) error {
	if gaps == nil {
		gaps = []streamstore.Gap{}
	}
	switch a.Out.Format() {
	case output.FormatJSONL:
		for _, p := range plays {
			if err := a.Out.Event("result", p); err != nil {
				return err
			}
		}
		for _, g := range gaps {
			if err := a.Out.Event("event", struct {
				Event string `json:"event"`
				streamstore.Gap
			}{"gap", g}); err != nil {
				return err
			}
		}
		return nil
	case output.FormatCSV:
		for _, g := range gaps {
			a.Out.Info("Note: %s", gapLine(g))
		}
		return a.Out.Result(plays, nil)
	}
	doc := historyDoc{Plays: plays, Gaps: gaps}
	if !from.IsZero() {
		doc.Since = &from
	}
	return a.Out.Result(doc, func(w io.Writer) {
		if len(plays) == 0 && len(gaps) == 0 {
			if noStreams {
				fmt.Fprintln(w, noStreamsHint)
			} else {
				fmt.Fprintln(w, "No plays recorded yet.")
			}
			return
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STARTED\tSTREAM\tARTIST — TITLE\tLENGTH")
		// Gaps are oldest first and plays newest first: show each gap
		// between the plays around it.
		pending := append([]streamstore.Gap(nil), gaps...)
		for _, p := range plays {
			for len(pending) > 0 && !pending[len(pending)-1].From.Before(p.Timestamp) {
				fmt.Fprintf(tw, "--\t\t%s\t\n", gapLine(pending[len(pending)-1]))
				pending = pending[:len(pending)-1]
			}
			fmt.Fprintf(tw, "%s\t%d\t%s — %s\t%s\n", p.Timestamp.Local().Format("2006-01-02 15:04"), p.RadioID, p.Artist, p.Title, fmtLength(p.PlayLength))
		}
		for i := len(pending) - 1; i >= 0; i-- {
			fmt.Fprintf(tw, "--\t\t%s\t\n", gapLine(pending[i]))
		}
		tw.Flush()
	})
}

type reportRowView struct {
	Key            string `json:"key"`
	Plays          int    `json:"plays"`
	AirtimeSeconds int64  `json:"airtime_seconds"`
	Stations       int    `json:"stations"`
}

type reportDoc struct {
	By       string            `json:"by"`
	Since    *time.Time        `json:"since"`
	Rows     []reportRowView   `json:"rows"`
	Gaps     []streamstore.Gap `json:"gaps"`
	Complete bool              `json:"complete"`
}

func newStreamsReportCmd(a *app.App) *cobra.Command {
	var by, since string
	var top int
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Count plays and airtime by song, artist, label, or station",
		Long: `Count recorded plays and total airtime, grouped by song, artist, label, or
station. Airtime adds up each play's length where AudD reported it.

When some streams were not recorded for part of the range, the report says so
("complete": false in JSON), because the totals then miss those plays.`,
		Example: `  audd streams report --by artist --since 30d
  audd streams report --by song --since 7d --top 20
  audd streams report --by station --format csv > stations.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			now := a.Now()
			from, err := parseSince(since, now)
			if err != nil {
				return err
			}
			st, err := openStore(a)
			if err != nil {
				return err
			}
			defer st.Close()
			if !contains([]string{"song", "artist", "label", "station"}, by) {
				return output.Errf(output.ExitUsage, "invalid_argument", "use --by song, artist, label, or station",
					"cannot group a report by %q", by)
			}
			refreshStore(cmd.Context(), a, st, nil)
			rows, err := st.Report(by, from)
			if err != nil {
				return err
			}
			gaps, noStreams, err := streamGaps(st, from, nil)
			if err != nil {
				return err
			}
			ensureRecorder(a)
			if noStreams && len(rows) == 0 && !a.Out.IsHuman() {
				a.Out.Info("%s", noStreamsHint)
			}
			if top > 0 && len(rows) > top {
				rows = rows[:top]
			}
			doc := reportDoc{By: by, Rows: make([]reportRowView, len(rows)), Gaps: gaps, Complete: len(gaps) == 0}
			if !from.IsZero() {
				doc.Since = &from
			}
			for i, r := range rows {
				key := r.Key
				if key == "" {
					key = "(unknown)"
				}
				doc.Rows[i] = reportRowView{Key: key, Plays: r.Plays, AirtimeSeconds: int64(r.Airtime / time.Second), Stations: r.Stations}
			}
			if f := a.Out.Format(); f == output.FormatCSV || f == output.FormatJSONL {
				if !doc.Complete {
					a.Out.Info("Note: %s, so these totals miss plays from that time. See: audd streams history", gapNote(gaps))
				}
				if f == output.FormatCSV {
					// The first column is named for the grouping ("artist"),
					// not "key".
					rows := make([]json.RawMessage, len(doc.Rows))
					for i, r := range doc.Rows {
						k, _ := json.Marshal(r.Key)
						byName, _ := json.Marshal(by)
						rows[i] = json.RawMessage(fmt.Sprintf(`{%s:%s,"plays":%d,"airtime_seconds":%d,"streams":%d}`, byName, k, r.Plays, r.AirtimeSeconds, r.Stations))
					}
					return a.Out.Result(rows, nil)
				}
				return a.Out.Result(doc.Rows, nil)
			}
			return a.Out.Result(doc, func(w io.Writer) {
				if len(doc.Rows) == 0 {
					if noStreams {
						fmt.Fprintln(w, noStreamsHint)
					} else {
						fmt.Fprintln(w, "No plays recorded in this range.")
					}
				} else {
					tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
					fmt.Fprintf(tw, "%s\tPLAYS\tAIRTIME\tSTREAMS\n", strings.ToUpper(by))
					for _, r := range doc.Rows {
						airtime := "-"
						if r.AirtimeSeconds > 0 {
							airtime = fmtDuration(time.Duration(r.AirtimeSeconds) * time.Second)
						}
						fmt.Fprintf(tw, "%s\t%d\t%s\t%d\n", r.Key, r.Plays, airtime, r.Stations)
					}
					tw.Flush()
				}
				if !doc.Complete {
					fmt.Fprintf(w, "\nNote: %s, so these totals miss plays from that time.\nSee the gaps with: audd streams history --since %s\n", gapNote(gaps), since)
				}
			})
		},
	}
	cmd.Flags().StringVar(&by, "by", "song", "group by song, artist, label, or station")
	_ = cmd.RegisterFlagCompletionFunc("by", cobra.FixedCompletions([]string{"song", "artist", "label", "station"}, cobra.ShellCompDirectiveNoFileComp))
	cmd.Flags().StringVar(&since, "since", "30d", `how far back: 24h, 7d, 30d, a date, or "all"`)
	cmd.Flags().IntVar(&top, "top", 0, "only the N rows with the most plays (0: all)")
	return cmd
}

func newStreamsExportCmd(a *app.App) *cobra.Command {
	var since string
	var id int
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export recorded plays as CSV or JSON lines",
		Long: `Export plays from the local stream store, oldest first. Piped output is JSON
lines by default; use --format csv for a spreadsheet.`,
		Example: `  audd streams export --since 30d --format csv > plays.csv
  audd streams export --since 2026-10-01 --id 1 > plays.jsonl`,
		Annotations: map[string]string{AnnotationStreaming: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := parseSince(since, a.Now())
			if err != nil {
				return err
			}
			var idp *int
			var ids []int
			if cmd.Flags().Changed("id") {
				idp, ids = &id, []int{id}
			}
			st, err := openStore(a)
			if err != nil {
				return err
			}
			defer st.Close()
			refreshStore(cmd.Context(), a, st, ids)
			plays, err := st.History(idp, from, 0)
			if err != nil {
				return err
			}
			for i, j := 0, len(plays)-1; i < j; i, j = i+1, j-1 {
				plays[i], plays[j] = plays[j], plays[i]
			}
			if gaps, noStreams, err := streamGaps(st, from, ids); err == nil && len(gaps) > 0 {
				a.Out.Info("Note: %s, so this export misses plays from that time. See: audd streams history", gapNote(gaps))
			} else if noStreams && len(plays) == 0 {
				a.Out.Info("%s", noStreamsHint)
			}
			ensureRecorder(a)
			return a.Out.Result(plays, nil)
		},
	}
	cmd.Flags().StringVar(&since, "since", "all", `how far back: 24h, 7d, 30d, a date, or "all"`)
	cmd.Flags().IntVar(&id, "id", 0, "only this stream")
	return cmd
}
