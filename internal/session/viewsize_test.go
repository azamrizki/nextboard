package session

import (
	"testing"
	"time"
)

// lastSize is the newest size message waiting on sub's channel, or nil.
func lastSize(sub *Sub) *SizeMsg {
	var got *SizeMsg
	for {
		select {
		case m := <-sub.C:
			if m.Size != nil {
				got = m.Size
			}
		default:
			return got
		}
	}
}

// Two views of one console: the one used last decides its size, the other only follows, so a
// program never draws for one view's size what the other shows at another.
func TestTheViewUsedLastDecidesTheSize(t *testing.T) {
	s := startingShell(t)
	_, card := s.Subscribe()
	_, tab := s.Subscribe()
	// the first view to say sets it; a second view saying another size does not take it over
	s.SubResize(card, 80, 24)
	if c, r := s.Size(); c != 80 || r != 24 {
		t.Fatalf("first view %dx%d", c, r)
	}
	s.SubResize(tab, 160, 40)
	if c, r := s.Size(); c != 80 || r != 24 {
		t.Fatalf("a second view took the size: %dx%d", c, r)
	}
	// every view hears the size it has to show
	if m := lastSize(tab); m == nil || m.Cols != 80 || m.Rows != 24 {
		t.Fatalf("tab heard %+v", m)
	}

	// typed into or focused, the second view decides; its size goes to every view
	s.Activate(tab)
	if c, r := s.Size(); c != 160 || r != 40 {
		t.Fatalf("after Activate %dx%d", c, r)
	}
	if m := lastSize(card); m == nil || m.Cols != 160 || m.Rows != 40 {
		t.Fatalf("card heard %+v", m)
	}
	// the first view resizing now changes nothing; the second one resizing does
	s.SubResize(card, 70, 20)
	if c, _ := s.Size(); c != 160 {
		t.Fatalf("the following view resized it to %d", c)
	}
	s.SubResize(tab, 150, 38)
	if c, r := s.Size(); c != 150 || r != 38 {
		t.Fatalf("the deciding view's resize %dx%d", c, r)
	}

	// the deciding view goes: the one used last of those left decides, at its own size
	time.Sleep(2 * time.Millisecond)
	s.Activate(card)
	s.Activate(tab)
	s.Unsubscribe(tab)
	if c, r := s.Size(); c != 70 || r != 20 {
		t.Fatalf("after the deciding view left %dx%d", c, r)
	}
	s.Unsubscribe(card)
}
