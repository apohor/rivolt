package rivian

import (
	"encoding/json"
	"sync/atomic"
	"testing"
)

// A frame that finds its subscription's channel full is dropped without
// blocking the reader, and the drop is counted; frames that fit are delivered
// and not counted.
func TestDeliverFrameCountsDrops(t *testing.T) {
	var drops atomic.Int64
	WSFrameDropObserver = func() { drops.Add(1) }
	defer func() { WSFrameDropObserver = nil }()

	m := &wsMux{}
	sub := &wsSub{id: "s", framesCh: make(chan json.RawMessage, 16), errCh: make(chan error, 1)}
	for i := 0; i < 16; i++ {
		if !m.deliverFrame(sub, json.RawMessage(`{}`)) {
			t.Fatalf("frame %d dropped with room in the channel", i)
		}
	}
	if drops.Load() != 0 {
		t.Fatalf("%d drops counted before the channel was full", drops.Load())
	}
	for i := 0; i < 3; i++ {
		if m.deliverFrame(sub, json.RawMessage(`{}`)) {
			t.Fatal("a frame was delivered into a full channel")
		}
	}
	if drops.Load() != 3 {
		t.Errorf("counted %d drops, want 3", drops.Load())
	}
	<-sub.framesCh
	if !m.deliverFrame(sub, json.RawMessage(`{}`)) {
		t.Error("frame refused after a slot freed up")
	}
}
