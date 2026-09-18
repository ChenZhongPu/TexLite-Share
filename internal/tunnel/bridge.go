package tunnel

import (
	"io"
	"net"
	"sync"
)

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
		_, _ = io.Copy(a, b)
	}()

	go func() {
		defer wg.Done()
		defer once.Do(closeBoth)
		_, _ = io.Copy(b, a)
	}()

	wg.Wait()
}
