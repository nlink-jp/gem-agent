package tui

import (
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// imageCatcher records the Image messages a Screen sends.
type imageCatcher struct {
	mu   sync.Mutex
	imgs []Image
}

func (c *imageCatcher) Send(msg tea.Msg) {
	if img, ok := msg.(Image); ok {
		c.mu.Lock()
		c.imgs = append(c.imgs, img)
		c.mu.Unlock()
	}
}

func (c *imageCatcher) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.imgs)
}

// TestScreenIsInertUntilItHasAProgram is the hop between the intake and the
// model, and it had no test at all — an independent pass found the whole
// route unpinned while Gate, the shape it was copied from, has seven.
//
// The MCP servers connect long before tea.NewProgram exists, so the intake
// is handed this rather than prog.Send (ADR-0090 §1). Unbound it must DROP
// what it is given: not panic, and not hold bytes to replay later into a
// session that may never have a UI.
func TestScreenIsInertUntilItHasAProgram(t *testing.T) {
	s := NewScreen()
	s.Image([]byte("bytes before there is anywhere to draw"), "image/png") // must not panic

	c := &imageCatcher{}
	s.SetProgram(c)
	if c.count() != 0 {
		t.Errorf("binding a program replayed %d dropped image(s); an entrance with no UI drops what it is given", c.count())
	}

	s.Image([]byte("after"), "image/png")
	if c.count() != 1 {
		t.Fatalf("a bound screen sent %d images, want 1", c.count())
	}
	if got := string(c.imgs[0].Data); got != "after" {
		t.Errorf("bytes arrived as %q", got)
	}
	if c.imgs[0].MIME != "image/png" {
		t.Errorf("MIME arrived as %q", c.imgs[0].MIME)
	}
}

// TestScreenBindsUnderConcurrentSends: the intake runs in the agent
// goroutine and SetProgram runs in the startup one, so the bind and a send
// can race. The mutex is what makes that safe; this fails under -race if it
// is removed.
func TestScreenBindsUnderConcurrentSends(t *testing.T) {
	s := NewScreen()
	c := &imageCatcher{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s.Image([]byte("x"), "image/png")
		}
	}()
	go func() {
		defer wg.Done()
		s.SetProgram(c)
	}()
	wg.Wait()
	// How many arrived depends on when the bind landed; that it did not
	// race is the assertion, and -race is the instrument.
	if c.count() > 50 {
		t.Errorf("more images arrived (%d) than were sent", c.count())
	}
}
