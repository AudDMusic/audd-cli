package cli

import (
	"encoding/json"
	"testing"
)

func TestMatchRowsLeadWithInputAndCached(t *testing.T) {
	raw := json.RawMessage(`[{"artist":"A","input":"theirs","title":"One","cached":false},{},"not an object"]`)
	rows := matchRows("mix.mp3", true, raw)
	want := []string{
		`{"input":"mix.mp3","cached":true,"artist":"A","title":"One"}`,
		`{"input":"mix.mp3","cached":true}`,
	}
	if len(rows) != len(want) {
		t.Fatalf("rows: %s", rows)
	}
	for i, r := range rows {
		if string(r) != want[i] {
			t.Errorf("row %d: %s", i, r)
		}
	}
}
