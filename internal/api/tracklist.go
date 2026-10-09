package api

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Track is a song that plays across one or more enterprise chunks.
type Track struct {
	Artist       string  `json:"artist"`
	Title        string  `json:"title"`
	Album        string  `json:"album,omitempty"`
	Label        string  `json:"label,omitempty"`
	ReleaseDate  string  `json:"release_date,omitempty"`
	ISRC         string  `json:"isrc,omitempty"`
	UPC          string  `json:"upc,omitempty"`
	SongLink     string  `json:"song_link,omitempty"`
	Score        int     `json:"score,omitempty"`
	Start        string  `json:"start"` // "h:mm:ss" or "m:ss"
	End          string  `json:"end"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Matches      int     `json:"matches"`

	lastChunk int // index into the chunk list of the last match
}

// chunkOf is the chunk a match came from: its offset in the file, which is
// the match's position minus its offset within the chunk.
func chunkOf(m Match) (float64, bool) {
	if m.StartSeconds == nil {
		return 0, false
	}
	return math.Round((*m.StartSeconds-float64(m.StartOffset)/1000)*100) / 100, true
}

func trackKey(m Match) string {
	return strings.ToLower(strings.TrimSpace(m.Artist)) + "\x00" + strings.ToLower(strings.TrimSpace(m.Title))
}

// Tracklist merges enterprise matches into tracks. Matches of the same
// artist and title in consecutive chunks become one track, starting at the
// first match's start and ending at the last match's end. One chunk with no
// match of that song may sit in between (a quiet passage or a missed
// chunk); a chunk with a different song ends the track. Several songs
// matched in the same chunk are tracked side by side. skip is the enterprise
// skip parameter (chunks skipped between scanned ones), so chunks that were
// never scanned are not counted as gaps.
func Tracklist(matches []Match, skip int) []Track {
	if skip < 0 {
		skip = 0
	}
	step := float64((skip + 1) * 12)
	// Group matches by chunk, in order.
	var groups []*chunkGroup
	byAt := map[float64]*chunkGroup{}
	for i, m := range matches {
		at, ok := chunkOf(m)
		if !ok {
			// Without positions, keep the response order: each match is its own chunk.
			g := &chunkGroup{at: float64(i), matches: []Match{m}}
			groups = append(groups, g)
			continue
		}
		g := byAt[at]
		if g == nil {
			g = &chunkGroup{at: at, pos: true}
			byAt[at] = g
			groups = append(groups, g)
		}
		g.matches = append(g.matches, m)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].pos != groups[j].pos {
			return groups[i].pos
		}
		return groups[i].pos && groups[i].at < groups[j].at
	})

	var tracks []*Track
	open := map[string]*Track{}
	for gi, g := range groups {
		for _, m := range g.matches {
			k := trackKey(m)
			t := open[k]
			if t != nil && canExtend(t, gi, groups, step) {
				t.Matches++
				if m.EndSeconds != nil && *m.EndSeconds > t.EndSeconds {
					t.EndSeconds = *m.EndSeconds
				}
				if m.Score > t.Score {
					t.Score = m.Score
				}
				fill(t, m)
				t.lastChunk = gi
				continue
			}
			nt := &Track{
				Artist: m.Artist, Title: m.Title, Album: m.Album, Label: m.Label, ReleaseDate: m.ReleaseDate,
				ISRC: m.ISRC, UPC: m.UPC, SongLink: m.SongLink, Score: m.Score, Matches: 1,
				lastChunk: gi,
			}
			if m.StartSeconds != nil {
				nt.StartSeconds = *m.StartSeconds
			}
			if m.EndSeconds != nil {
				nt.EndSeconds = *m.EndSeconds
			}
			open[k] = nt
			tracks = append(tracks, nt)
		}
	}
	out := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		t.StartSeconds = round3(t.StartSeconds)
		t.EndSeconds = round3(t.EndSeconds)
		t.Start = Clock(t.StartSeconds)
		t.End = Clock(t.EndSeconds)
		out = append(out, *t)
	}
	return out
}

type chunkGroup struct {
	at      float64
	pos     bool
	matches []Match
}

// canExtend reports whether track t continues into chunk gi: the same chunk,
// or the next chunk that had any match, at most one scanned chunk later.
func canExtend(t *Track, gi int, groups []*chunkGroup, step float64) bool {
	last := t.lastChunk
	switch {
	case gi == last:
		return true
	case gi != last+1:
		return false // other songs played in between
	case !groups[gi].pos || !groups[last].pos:
		return !groups[gi].pos && !groups[last].pos
	}
	return groups[gi].at-groups[last].at <= 2*step+0.5
}

func fill(t *Track, m Match) {
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&t.Album, m.Album}, {&t.Label, m.Label}, {&t.ReleaseDate, m.ReleaseDate},
		{&t.ISRC, m.ISRC}, {&t.UPC, m.UPC}, {&t.SongLink, m.SongLink},
	} {
		if *f.dst == "" {
			*f.dst = f.src
		}
	}
}

// Clock formats seconds as "m:ss", or "h:mm:ss" from an hour on.
func Clock(sec float64) string {
	s := int(sec)
	if s < 0 {
		s = 0
	}
	h, m, ss := s/3600, s%3600/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, ss)
	}
	return fmt.Sprintf("%d:%02d", m, ss)
}
