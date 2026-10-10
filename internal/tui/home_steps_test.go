package tui

import (
	"strings"
	"testing"
)

func TestEveryGettingStartedStepGoesBack(t *testing.T) {
	for i := range gettingStarted {
		h := sized(newTestHome(t, &fakeRun{}, homeOpts{}), 120, 40)
		h.show("help", true)
		hs := h.subs["help"].(*helpSection)
		hs.setPage(0)
		hs.stepCur = i
		press(h, key("enter"))
		landed := h.activeID()
		if h.paletteO {
			landed = "palette"
		}
		for n := 0; n < 6 && (h.activeID() != "help" || h.paletteO); n++ {
			press(h, key("esc"))
		}
		if h.activeID() != "help" || hs.page != 0 || hs.stepCur != i || h.focus != focusContent {
			t.Errorf("step %d %q (opened %s): esc did not return to the step (active %s page %d step %d focus %v)", i, gettingStarted[i].title, landed, h.activeID(), hs.page, hs.stepCur, h.focus)
			continue
		}
		press(h, key("esc"))
		if h.focus != focusSidebar || !strings.Contains(h.View(), ">7 Help") && !strings.Contains(h.View(), "Help") {
			t.Errorf("step %d: second esc did not reach the sidebar", i)
		}
		t.Logf("step %d %q -> %s -> back to Help -> sidebar: ok", i, gettingStarted[i].title, landed)
	}
}
