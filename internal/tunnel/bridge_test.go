package tunnel_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"

	"texlite-share/internal/tunnel"
)

func TestBridge_BidirectionalTransfer(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()

	go tunnel.Bridge(a2, b1)

	// Send data from a1 -> b2
	dataA := []byte("hello from A")
	go func() {
		_, _ = a1.Write(dataA)
	}()

	bufB := make([]byte, len(dataA))
	if _, err := io.ReadFull(b2, bufB); err != nil {
		t.Fatalf("failed reading from b2: %v", err)
	}
	if !bytes.Equal(bufB, dataA) {
		t.Fatalf("expected %q, got %q", dataA, bufB)
	}

	// Send data from b2 -> a1
	dataB := []byte("hello from B")
	go func() {
		_, _ = b2.Write(dataB)
	}()

	bufA := make([]byte, len(dataB))
	if _, err := io.ReadFull(a1, bufA); err != nil {
		t.Fatalf("failed reading from a1: %v", err)
	}
	if !bytes.Equal(bufA, dataB) {
		t.Fatalf("expected %q, got %q", dataB, bufA)
	}

	// Close a1 and verify that b2 closes
	_ = a1.Close()

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 10)
		_, _ = b2.Read(buf)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bridge to close opposite side")
	}
	_ = b2.Close()
}

func TestBridge_LargePayload(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()

	go tunnel.Bridge(a2, b1)

	size := 1024 * 1024 // 1 MB
	srcData := make([]byte, size)
	_, _ = rand.Read(srcData)

	errCh := make(chan error, 2)
	go func() {
		_, err := a1.Write(srcData)
		_ = a1.Close()
		errCh <- err
	}()

	var received bytes.Buffer
	go func() {
		_, err := io.Copy(&received, b2)
		_ = b2.Close()
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && err != io.EOF {
			t.Fatalf("unexpected error during large transfer: %v", err)
		}
	}

	if !bytes.Equal(srcData, received.Bytes()) {
		t.Fatal("streamed data mismatch")
	}
}
