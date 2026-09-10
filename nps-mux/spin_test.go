package nps_mux

import (
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// A dequeue whose head has run past slots that will never be filled is the
// state a second concurrent consumer can leave behind (popTail claims a slot
// by CAS-ing the slot pointer but advances the tail unconditionally).
// popTail must report the queue as empty instead of retrying forever.
func TestPopTailGivesUpOnDesyncedDequeue(t *testing.T) {
	d := new(bufDequeue)
	d.vals = make([]unsafe.Pointer, 8)
	atomic.StoreUint64(&d.headTail, d.pack(4, 0)) // head != tail, every slot nil

	done := make(chan bool, 1)
	go func() {
		_, ok := d.popTail()
		done <- ok
	}()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("popTail returned a value from a dequeue holding none")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("popTail spun for 3s on a desynced dequeue: the busy loop is back")
	}
}

// receiveWindowQueue.Push bumps the length counter before it links the element,
// and receiveWindow.release drains the same chain concurrently with Read, so
// the counter can claim data the chain will never produce. Pop must fall back
// to its wait path instead of retrying forever.
func TestReceiveWindowQueuePopRecoversFromCounterDrift(t *testing.T) {
	q := newReceiveWindowQueue()
	atomic.StoreUint64(&q.lengthWait, q.chain.head.pack(1024, 0)) // 1024 bytes that do not exist
	q.SetTimeOut(time.Now().Add(500 * time.Millisecond))

	done := make(chan error, 1)
	go func() {
		_, err := q.Pop()
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a read timeout from the wait path, got a value")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pop spun for 5s instead of falling back to the wait path: the busy loop is back")
	}
}

// The happy path must keep working: what goes in comes back out, in order.
func TestReceiveWindowQueueRoundTripStillWorks(t *testing.T) {
	q := newReceiveWindowQueue()
	for i := 0; i < 100; i++ {
		buf := make([]byte, 4)
		ele, err := newListElement(buf, 4, false)
		if err != nil {
			t.Fatal(err)
		}
		q.Push(ele)
	}
	for i := 0; i < 100; i++ {
		ele, err := q.Pop()
		if err != nil {
			t.Fatalf("pop %d: %v", i, err)
		}
		if ele.L != 4 {
			t.Fatalf("pop %d: length %d, want 4", i, ele.L)
		}
	}
	if n := q.Len(); n != 0 {
		t.Fatalf("queue length %d after draining, want 0", n)
	}
}
