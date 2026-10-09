package cli

import (
	"context"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/jobs"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// The explorer's Jobs tab reads the real jobs database.
func TestExplorerDataReadsJobs(t *testing.T) {
	testutil.Isolate(t)
	st, err := jobs.Open()
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.Create([]string{"audd", "recognize", "./music"}, []media.Input{{Path: "/music/a.mp3"}}, nil)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	d, err := newExplorerData(app.New())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.Jobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != job.ID || rows[0].Total != 1 {
		t.Fatalf("jobs tab rows: %+v", rows)
	}
	items, err := d.JobItems(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Input != "/music/a.mp3" {
		t.Fatalf("job items: %+v", items)
	}
	if _, err := d.Recent(context.Background(), 10); err != nil {
		t.Fatalf("recent from an empty cache: %v", err)
	}
}
