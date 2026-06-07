package agent

import "github.com/sgrankin/wave/internal/server"

// StepForTest exposes the internal per-update step so tests can drive the loop
// deterministically (one update at a time) instead of racing the Run goroutine.
func (r *Runtime) StepForTest(u server.WaveletUpdate) { r.step(u) }
