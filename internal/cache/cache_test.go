package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/media"
)

func openTemp(t *testing.T) *Cache {
	t.Helper()
	c, err := OpenAt(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRenamedFileHitsCache(t *testing.T) {
	c := openTemp(t)
	dir := t.TempDir()
	a := write(t, dir, "a.mp3", "same audio bytes")
	b := write(t, dir, "renamed copy.mp3", "same audio bytes")
	other := write(t, dir, "other.mp3", "different audio")

	ka, err := KeyForFile(a, EndpointStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ka, false, json.RawMessage(`{"artist":"A"}`)); err != nil {
		t.Fatal(err)
	}
	kb, _ := KeyForFile(b, EndpointStandard, map[string]string{})
	if got, ok := c.Get(kb); !ok || string(got) != `{"artist":"A"}` {
		t.Fatalf("renamed file should hit: %s %v", got, ok)
	}
	ko, _ := KeyForFile(other, EndpointStandard, nil)
	if _, ok := c.Get(ko); ok {
		t.Fatal("different bytes must miss")
	}
	// Endpoint and parameters are part of the key; order does not matter.
	ke, _ := KeyForFile(a, EndpointEnterprise, nil)
	kr1, _ := KeyForFile(a, EndpointStandard, map[string]string{"return": "spotify", "market": "us"})
	kr2, _ := KeyForFile(a, EndpointStandard, map[string]string{"market": "us", "return": "spotify"})
	if ke == ka || kr1 == ka || kr1 != kr2 {
		t.Fatal("key must depend on endpoint and params, not param order")
	}
	// Empty params are the same as missing ones.
	kEmpty, _ := KeyForFile(a, EndpointStandard, map[string]string{"return": ""})
	if kEmpty != ka {
		t.Fatal("empty param should not change the key")
	}
	ki, _ := KeyForInput(media.Input{Path: b}, EndpointStandard, nil)
	if ki != ka {
		t.Fatal("KeyForInput(file) should match KeyForFile")
	}
}

func TestURLEntriesExpire(t *testing.T) {
	c := openTemp(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return now })
	ku := KeyForURL("https://example.com/a.mp3", EndpointStandard, nil)
	kf := KeyForURL("https://example.com/b.mp3", EndpointStandard, nil)
	if ku == kf {
		t.Fatal("different URLs share a key")
	}
	if err := c.Put(ku, true, json.RawMessage(`null`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("filekey", false, json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(23 * time.Hour)
	if got, ok := c.Get(ku); !ok || string(got) != "null" {
		t.Fatal("URL entry should be valid for 24h (no-match results are cached too)")
	}
	now = now.Add(2 * time.Hour)
	if _, ok := c.Get(ku); ok {
		t.Fatal("URL entry should expire after 24h")
	}
	if _, ok := c.Get("filekey"); !ok {
		t.Fatal("file entries do not expire")
	}
}

func TestRecentAndClear(t *testing.T) {
	c := openTemp(t)
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for i, name := range []string{"one.mp3", "two.mp3", "three.mp3"} {
		if err := c.PutEntry(Entry{Key: name, Source: name, Endpoint: EndpointStandard, At: base.Add(time.Duration(i) * time.Minute), Result: json.RawMessage(`{}`)}, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.Recent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Source != "three.mp3" || got[1].Source != "two.mp3" || got[0].Endpoint != EndpointStandard {
		t.Fatalf("recent: %+v", got)
	}
	if n, _ := c.Count(); n != 3 {
		t.Fatalf("count %d", n)
	}
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Count(); n != 0 {
		t.Fatalf("after clear %d", n)
	}
}

// A no-match (null from the standard endpoint, [] from the enterprise one)
// is asked again after NoMatchTTL; a match stays.
func TestNoMatchesExpire(t *testing.T) {
	c := openTemp(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return now })
	for key, res := range map[string]string{"none": `null`, "none-enterprise": `[]`, "match": `{"artist":"A"}`} {
		if err := c.Put(key, false, json.RawMessage(res)); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(NoMatchTTL - time.Hour)
	for _, key := range []string{"none", "none-enterprise", "match"} {
		if _, ok := c.Get(key); !ok {
			t.Fatalf("%s: valid within %v", key, NoMatchTTL)
		}
	}
	now = now.Add(2 * time.Hour)
	for _, key := range []string{"none", "none-enterprise"} {
		if _, ok := c.Get(key); ok {
			t.Fatalf("%s: a no-match expires after %v", key, NoMatchTTL)
		}
	}
	if _, ok := c.Get("match"); !ok {
		t.Fatal("a match does not expire")
	}
}

// Forget drops every result for an input, whatever the endpoint and the
// parameters, and nothing else.
func TestForgetAnInput(t *testing.T) {
	c := openTemp(t)
	dir := t.TempDir()
	song := media.Input{Path: write(t, dir, "song.mp3", "song bytes")}
	copyOf := media.Input{Path: write(t, dir, "copy.mp3", "song bytes")}
	other := media.Input{Path: write(t, dir, "other.mp3", "other bytes")}
	id, err := InputID(song)
	if err != nil {
		t.Fatal(err)
	}
	if cid, _ := InputID(copyOf); cid != id {
		t.Fatal("a copy is the same input")
	}
	if k, _ := KeyForInput(song, EndpointStandard, map[string]string{"account": "x"}); k != Key(id, EndpointStandard, map[string]string{"account": "x"}) {
		t.Fatal("KeyForInput is Key of the InputID")
	}
	otherID, _ := InputID(other)
	put := func(content, endpoint string, params map[string]string) string {
		k := Key(content, endpoint, params)
		if err := c.PutEntry(Entry{Key: k, Endpoint: endpoint, Content: content, Result: json.RawMessage(`null`)}, false); err != nil {
			t.Fatal(err)
		}
		return k
	}
	gone := []string{
		put(id, EndpointStandard, map[string]string{"account": "a"}),
		put(id, EndpointStandard, map[string]string{"account": "b", "return": "spotify"}),
		put(id, EndpointEnterprise, map[string]string{"account": "a", "limit": "3"}),
	}
	kept := put(otherID, EndpointStandard, map[string]string{"account": "a"})
	if n, err := c.Forget(id); err != nil || n != 3 {
		t.Fatalf("forget: %d %v", n, err)
	}
	for _, k := range gone {
		if _, ok := c.Get(k); ok {
			t.Fatal("still cached")
		}
	}
	if _, ok := c.Get(kept); !ok {
		t.Fatal("another input was forgotten")
	}
	if uid, _ := InputID(media.Input{URL: " https://example.com/a.mp3 "}); uid != "url\x00https://example.com/a.mp3" {
		t.Fatalf("URL id %q", uid)
	}
}
