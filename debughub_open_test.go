package agentproxy

import (
	"testing"
	"time"
)

// An open event fired with no UI observers connected must be queued and
// handed to the next observer (via takePendingOpen), exactly once.
func TestSendOrQueueOpenQueuesWhenNoObservers(t *testing.T) {
	h := NewDebugHub()
	msg := []byte(`{"t":"open","url":"https://example.com/login"}`)
	h.SendOrQueueOpen(msg)

	got := h.takePendingOpen()
	if string(got) != string(msg) {
		t.Fatalf("takePendingOpen = %q, want %q", got, msg)
	}
	// Delivered at most once: a second take returns nothing.
	if again := h.takePendingOpen(); again != nil {
		t.Fatalf("second takePendingOpen = %q, want nil", again)
	}
}

// A queued open event older than openReplayTTL is dropped, not replayed --
// a login URL from minutes ago should not pop a dialog on a fresh page.
func TestTakePendingOpenExpires(t *testing.T) {
	h := NewDebugHub()
	h.openReplayTTL = 10 * time.Millisecond
	h.SendOrQueueOpen([]byte(`{"t":"open","url":"https://example.com"}`))
	time.Sleep(20 * time.Millisecond)
	if got := h.takePendingOpen(); got != nil {
		t.Fatalf("expired takePendingOpen = %q, want nil", got)
	}
}

// A newer open event replaces an older queued one -- only the latest URL
// is replayed.
func TestSendOrQueueOpenKeepsLatest(t *testing.T) {
	h := NewDebugHub()
	h.SendOrQueueOpen([]byte(`{"t":"open","url":"https://old.example.com"}`))
	latest := []byte(`{"t":"open","url":"https://new.example.com"}`)
	h.SendOrQueueOpen(latest)
	if got := h.takePendingOpen(); string(got) != string(latest) {
		t.Fatalf("takePendingOpen = %q, want latest %q", got, latest)
	}
}
