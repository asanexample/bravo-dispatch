package awsqueue

import (
	"context"
	"testing"
	"time"
)

func TestOpenEmptyURLReturnsMemory(t *testing.T) {
	ctx := context.Background()
	q, err := Open(ctx, "")
	if err != nil {
		t.Fatalf("Open(\"\"): %v", err)
	}
	if q.Backend() != "memory" {
		t.Errorf("Backend() = %q, want memory", q.Backend())
	}
}

func TestMemorySendReceiveDeleteRoundTrip(t *testing.T) {
	q := NewMemory()
	ctx := context.Background()

	if err := q.Send(ctx, `{"shipmentId":"BD-10023"}`); err != nil {
		t.Fatalf("Send: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	msgs, err := q.Receive(recvCtx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Receive returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Body != `{"shipmentId":"BD-10023"}` {
		t.Errorf("Body = %q, want the sent body", msgs[0].Body)
	}
	if msgs[0].ReceiptHandle == "" {
		t.Error("ReceiptHandle is empty")
	}

	if err := q.Delete(ctx, msgs[0].ReceiptHandle); err != nil {
		t.Errorf("Delete: %v (memory mode should always succeed)", err)
	}
}

// TestMemoryReceiveBlocksUntilSendOrCancel proves the memory queue's Receive genuinely blocks (a long-poll
// contract) rather than returning immediately with an empty result — the property dispatch-worker's
// background loop depends on to avoid busy-looping when the queue is empty.
func TestMemoryReceiveBlocksUntilSendOrCancel(t *testing.T) {
	q := NewMemory()

	t.Run("cancelled context returns an error, not an empty slice", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := q.Receive(ctx); err == nil {
			t.Error("Receive on an empty queue with an expiring context returned nil error, want ctx.Err()")
		}
	})

	t.Run("a concurrent Send unblocks Receive", func(t *testing.T) {
		done := make(chan []Message, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			msgs, err := q.Receive(ctx)
			if err != nil {
				t.Errorf("Receive: %v", err)
			}
			done <- msgs
		}()

		time.Sleep(20 * time.Millisecond) // give Receive time to start blocking
		if err := q.Send(context.Background(), "hello"); err != nil {
			t.Fatalf("Send: %v", err)
		}

		select {
		case msgs := <-done:
			if len(msgs) != 1 || msgs[0].Body != "hello" {
				t.Errorf("Receive() = %+v, want one message with body \"hello\"", msgs)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Receive never unblocked after a Send")
		}
	})
}
