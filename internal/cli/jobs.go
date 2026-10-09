package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/jobs"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newJobsCmd(a))
	})
}

func newJobsCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List, inspect, and resume batch recognitions",
		Long: `Every batch recognition (a folder, glob, list, or several files) is a job.
Each file's result is saved as soon as it arrives, so Ctrl-C, a crash, or a
dropped connection never loses finished work, and resuming never sends a
finished file again.

Files whose request may have reached AudD before the failure, and files
that would fail the same way again, are not sent again automatically; resume
with --retry-failed to send them.`,
		GroupID: GroupLocal,
		Example: `  audd jobs list
  audd jobs show k7m2xq
  audd jobs show k7m2xq --failed
  audd jobs resume k7m2xq
  audd jobs resume k7m2xq --retry-failed
  audd jobs clean --older-than 30d`,
	}
	cmd.AddCommand(newJobsListCmd(a), newJobsShowCmd(a), newJobsBrowseCmd(a), newJobsResumeCmd(a), newJobsCleanCmd(a))
	return cmd
}

func jobsOpen() (*jobs.Store, error) {
	st, err := jobs.Open()
	if err != nil {
		return nil, output.AsError(err)
	}
	return st, nil
}

func newJobsListCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List jobs, newest first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := jobsOpen()
			if err != nil {
				return err
			}
			defer st.Close()
			list, err := jobs.List(st)
			if err != nil {
				return output.AsError(err)
			}
			if list == nil {
				list = []jobs.Job{}
			}
			return a.Out.Result(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No jobs yet. Recognizing a folder starts one, for example: audd recognize ./music --max-files 100")
					return
				}
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tSTATUS\tDONE\tFAILED\tENDPOINT\tCREATED\tINPUTS")
				for _, j := range list {
					fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%d\t%s\t%s\t%s\n", j.ID, j.Status, j.Done, j.Total, j.Failed,
						j.Params["endpoint"], j.Created.Local().Format("2006-01-02 15:04"), j.Inputs)
				}
				tw.Flush()
			})
		},
	}
}

// jobsShowDoc is `audd jobs show` as one JSON document.
type jobsShowDoc struct {
	jobs.Job
	Items []jobs.ResultLine `json:"items"`
}

func newJobsShowCmd(a *app.App) *cobra.Command {
	var failedOnly bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a job and the result of every file",
		Long: `Show a job and the result of every file.

As JSON it is one document with the job and its items; as JSONL one result
line per file; as CSV one row per match.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return jobsShow(a, args[0], failedOnly)
		},
	}
	cmd.Flags().BoolVar(&failedOnly, "failed", false, "show only the files that failed")
	return cmd
}

func jobsShow(a *app.App, id string, failedOnly bool) error {
	st, err := jobsOpen()
	if err != nil {
		return err
	}
	defer st.Close()
	job, err := st.Get(id)
	if err != nil {
		return err
	}
	items, err := jobs.Results(st, id)
	if err != nil {
		return output.AsError(err)
	}
	if failedOnly {
		kept := items[:0]
		for _, it := range items {
			if it.State == jobs.StateFailed {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	switch a.Out.Format() {
	case output.FormatCSV:
		rows := []jobs.CSVRow{}
		for _, it := range items {
			rows = append(rows, jobs.Rows(job.ID, it)...)
		}
		return a.Out.Result(rows, nil)
	case output.FormatJSONL:
		ls := make([]jobs.ResultLine, 0, len(items))
		for _, it := range items {
			ls = append(ls, jobs.Line(job.ID, it))
		}
		return a.Out.Result(ls, nil)
	}
	doc := jobsShowDoc{Job: *job, Items: make([]jobs.ResultLine, 0, len(items))}
	for _, it := range items {
		doc.Items = append(doc.Items, jobs.Line(job.ID, it))
	}
	return a.Out.Result(doc, func(w io.Writer) {
		st := a.Out.Styles()
		fmt.Fprintf(w, "%s %s  %s\n", st.Title.Render("Job "+job.ID), st.Dim.Render(job.Created.Local().Format("2006-01-02 15:04")), job.Status)
		fmt.Fprintf(w, "%s of %s files done: %s recognized, %s no match, %s failed, %s cached; %s.\n",
			output.Thousands(job.Done), output.Thousands(job.Total), output.Thousands(job.Done-job.NoMatch),
			output.Thousands(job.NoMatch), output.Thousands(job.Failed), output.Thousands(job.Cached),
			jobs.RequestsUsed(job.Requests, job.RequestsReserved, job.RequestsUnknownFiles))
		fmt.Fprintln(w)
		for _, it := range items {
			jobs.HumanLine(w, st, it)
		}
		if n := job.BudgetLimitedFiles; n > 0 {
			fmt.Fprintf(w, "\n%s may be only partly recognized: --max-requests lowered the limit sent for them. %s\n",
				output.Plural(n, "file"), jobs.BudgetLimitedHint)
		}
		switch {
		case job.Status == jobs.StatusRunning:
		case job.Pending > 0:
			fmt.Fprintf(w, "\nResume with: audd jobs resume %s\n", job.ID)
		case job.Failed > 0:
			fmt.Fprintf(w, "\nRetry the failed files with: audd jobs resume %s --retry-failed\n", job.ID)
		}
	})
}

func newJobsBrowseCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "browse <id>",
		Short: "Explore a job's results interactively",
		Long:  "Open the interactive explorer on this job. Without a terminal it prints the job like audd jobs show.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := jobsOpen()
			if err != nil {
				return err
			}
			_, err = st.Get(args[0])
			st.Close()
			if err != nil {
				return err
			}
			if a.Out.Options().StdoutTTY && a.Out.IsHuman() {
				err := app.RunExplorer(cmd.Context(), a, "jobs/"+args[0])
				var oe *output.Error
				if !errors.As(err, &oe) || oe.Code != "not_implemented" {
					return err
				}
			}
			return jobsShow(a, args[0], false)
		},
	}
}

func newJobsResumeCmd(a *app.App) *cobra.Command {
	var retryFailed, dryRun bool
	var concurrency int
	cmd := &cobra.Command{
		Use:   "resume <id>",
		Short: "Continue a job where it stopped",
		Long: `Continue a job where it stopped. Finished files are not sent again.

Files that failed before reaching AudD (a connection that never opened, a
file that was missing) are retried automatically. Other failures, including
files whose request may have reached AudD, are retried only with
--retry-failed.

Results stream as each file finishes: with --format json or jsonl (the
default when piped) that is JSONL, one "result" line per file and a final
"summary" line, not one JSON document. audd jobs show prints the whole job
as one document.`,
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{AnnotationStreaming: "true"},
		Example: `  audd jobs resume k7m2xq
  audd jobs resume k7m2xq --retry-failed --yes
  audd jobs resume k7m2xq --max-requests 500`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if concurrency < 0 {
				return output.Errf(output.ExitUsage, "invalid_argument", "", "--concurrency must be 1 or more")
			}
			_, err := app.RunBatch(cmd.Context(), a, app.BatchOptions{
				ResumeID: args[0], RetryFailed: retryFailed, DryRun: dryRun,
				Concurrency: concurrency, Yes: a.Flags.Yes,
			})
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&retryFailed, "retry-failed", false, "also retry the other failed files, including files whose earlier request may have reached AudD")
	f.BoolVar(&dryRun, "dry-run", false, "show what resuming would send, and stop")
	f.IntVar(&concurrency, "concurrency", 0, "parallel requests (default: the concurrency setting, else 4)")
	return cmd
}

func newJobsCleanCmd(a *app.App) *cobra.Command {
	var olderThan string
	var all bool
	cmd := &cobra.Command{
		Use:   "clean [<id>…]",
		Short: "Delete old jobs from this computer",
		Long: `Delete jobs and their saved results from this computer. Without IDs it
deletes jobs not updated for --older-than (default 30d); --all deletes every
job. Running jobs are kept.`,
		Example: `  audd jobs clean
  audd jobs clean --older-than 7d
  audd jobs clean k7m2xq
  audd jobs clean --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := jobsOpen()
			if err != nil {
				return err
			}
			defer st.Close()
			n := 0
			switch {
			case len(args) > 0:
				if all || cmd.Flags().Changed("older-than") {
					return output.Errf(output.ExitUsage, "invalid_argument", "", "give job IDs or --older-than/--all, not both")
				}
				for _, id := range args {
					if err := st.Delete(id); err != nil {
						return err
					}
					n++
				}
			default:
				age := time.Duration(0)
				if !all {
					if age, err = jobsParseAge(olderThan); err != nil {
						return err
					}
				}
				if n, err = st.Clean(age); err != nil {
					return output.AsError(err)
				}
			}
			return a.Out.Result(struct {
				Deleted int `json:"deleted"`
			}{n}, func(w io.Writer) {
				switch n {
				case 0:
					fmt.Fprintln(w, "No jobs to delete.")
				case 1:
					fmt.Fprintln(w, "Deleted 1 job.")
				default:
					fmt.Fprintf(w, "Deleted %d jobs.\n", n)
				}
			})
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "30d", "delete jobs not updated for this long (e.g. 12h, 7d)")
	cmd.Flags().BoolVar(&all, "all", false, "delete every job that is not running")
	return cmd
}

// jobsParseAge parses durations like 90m, 12h, 7d, 2w.
func jobsParseAge(s string) (time.Duration, error) {
	bad := output.Errf(output.ExitUsage, "invalid_argument", "use a number with m, h, d, or w, e.g. 7d", "cannot read duration %q", s)
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return 0, bad
	}
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	n, err := strconv.Atoi(s[:len(s)-1])
	if unit == 0 || err != nil || n < 0 {
		return 0, bad
	}
	return time.Duration(n) * unit, nil
}
