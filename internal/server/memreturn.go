package server

import (
	"context"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// Spikes leave the hub's heap far bigger than what it holds: a login's password hash takes
// 64 MiB, each console's screen 4 MiB as it starts, a review copy tens of MB. Go gives such room
// back to the system slowly, and a hub idle at a few MB of heap kept 50 to 100 MB of it for
// minutes. Once a minute, room above returnAbove that the heap does not use goes back at once.
const (
	returnEvery = time.Minute
	returnAbove = 32 << 20
)

func (s *Server) returnMemory(ctx context.Context) {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/free:bytes"}}
	t := time.NewTicker(returnEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		metrics.Read(sample)
		if sample[0].Value.Kind() == metrics.KindUint64 && sample[0].Value.Uint64() > returnAbove {
			debug.FreeOSMemory()
		}
	}
}
