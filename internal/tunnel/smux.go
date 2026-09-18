package tunnel

import (
	"time"

	"github.com/xtaci/smux"
)

// SmuxConfig returns a properly configured smux v2 Config conforming to the specification.
func SmuxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.Version = 2
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.KeepAliveTimeout = 45 * time.Second
	cfg.MaxReceiveBuffer = 4 * 1024 * 1024 // 4 MB
	cfg.MaxStreamBuffer = 1 * 1024 * 1024  // 1 MB
	return cfg
}
