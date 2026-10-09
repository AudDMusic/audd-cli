package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/output"
)

func TestDefaultsReportNotImplemented(t *testing.T) {
	a := New()
	check := func(name string, err error) {
		t.Helper()
		var oe *output.Error
		if !errors.As(err, &oe) || oe.Code != "not_implemented" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	_, err := a.APIClient()
	check("APIClient", err)
	_, err = a.Account()
	check("Account", err)
	_, err = RunBatch(context.Background(), a, BatchOptions{})
	check("RunBatch", err)
	check("RunExplorer", RunExplorer(context.Background(), a, "recent"))
	_, err = EnsureRecorder(a)
	check("EnsureRecorder", err)
	if a.Now == nil {
		t.Fatal("Now must default to time.Now")
	}
}

func TestPlainResult(t *testing.T) {
	var b bytes.Buffer
	RenderResult(&b, New(), ResultView{
		Artist: "Imagine Dragons", Title: "Warriors", Album: "Smoke + Mirrors", Label: "KIDinaKORNER",
		ReleaseDate: "2014-09-18", SongLink: "https://lis.tn/Warriors", ISRC: "USUM71414155", Score: 100, Cached: true,
	}, true)
	want := "Imagine Dragons — Warriors\nSmoke + Mirrors · KIDinaKORNER · 2014-09-18\nhttps://lis.tn/Warriors\nISRC:     USUM71414155\nScore:    100\n(cached result, no request used)\n"
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}
