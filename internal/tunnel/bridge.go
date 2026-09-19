package tunnel

import (
	"io"
	"net"
	"sync"
)

var bridgeBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024) // 64 KB buffer reduces syscalls and small chunks
		return &b
	},
}

// Bridge continuously copies data bidirectionally between connections a and b.
// When either direction encounters EOF or an error, both connections are closed,
// terminating both transfer loops without leaking goroutines.
func Bridge(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		_ = a.Close()
		_ = b.Close()
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer once.Do(closeBoth)
		bufPtr := bridgeBufPool.Get().(*[]byte)
		defer bridgeBufPool.Put(bufPtr)
		_, _ = io.CopyBuffer(a, b, *bufPtr)
	}()

	go func() {
		defer wg.Done()
		defer once.Do(closeBoth)
		bufPtr := bridgeBufPool.Get().(*[]byte)
		defer bridgeBufPool.Put(bufPtr)
		_, _ = io.CopyBuffer(b, a, *bufPtr)
	}()

	wg.Wait()
}
