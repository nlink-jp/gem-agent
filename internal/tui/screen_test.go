package tui

import (
	"sync"
	"testing"
	"time"

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

// waitFor polls until n images have arrived. The send is asynchronous —
// it has to be, or a caller inside Update deadlocks the UI — so delivery
// is not observable on the calling goroutine's next line.
func (c *imageCatcher) waitFor(n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.count() >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
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
	if !c.waitFor(1, 2*time.Second) {
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
	// How many arrived depends on when the bind landed and on when the
	// sends drain; that it did not race is the assertion, and -race is
	// the instrument.
	time.Sleep(50 * time.Millisecond)
	if c.count() > 50 {
		t.Errorf("more images arrived (%d) than were sent", c.count())
	}
}

// blockingSender never returns from Send, the way Bubble Tea's Program
// does not while Update is still running: Send blocks on the channel the
// event loop drains, and the event loop cannot drain it until Update
// returns.
type blockingSender struct{ entered chan struct{} }

func (b *blockingSender) Send(tea.Msg) {
	close(b.entered)
	select {} // never returns, like the deadlock this pins
}

// TestScreenDoesNotBlockItsCaller is the freeze, pinned. The slash handler
// runs INSIDE Update (submit -> m.slash), so /show reached Program.Send
// from the one goroutine that must return first, and the whole UI locked
// up — keystrokes included, so neither Ctrl+C nor Ctrl+D could end it. It
// had to be killed from outside.
//
// The fix is that Image never blocks its caller. This test fails by
// timing out against the version that sends inline.
func TestScreenDoesNotBlockItsCaller(t *testing.T) {
	s := NewScreen()
	b := &blockingSender{entered: make(chan struct{})}
	s.SetProgram(b)

	returned := make(chan bool, 1)
	go func() { returned <- s.Image([]byte("bytes"), "image/png") }()

	select {
	case ok := <-returned:
		if !ok {
			t.Error("a bound screen reported no program")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Image blocked its caller — from Update this deadlocks the whole UI, " +
			"and the session can only be killed from outside")
	}
	// And it really did try to send: the bool is not a lie about delivery.
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Error("the send never started")
	}
}
