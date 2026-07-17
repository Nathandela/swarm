package engine

import "time"

// SampleCPU is the production CPU sampler the engine installs when
// Config.CPUSampler is nil (E10.6). It returns a per-process utilization as a
// percentage of one core: an idle process reads ~0 and a busy one reads clearly
// positive. It samples cumulative process CPU time twice across cpuSampleWindow
// and reports the rate over that window, so a single call yields a utilization
// without any caller-held state. Its per-platform implementation lives in the
// build-tagged cpu_darwin.go / cpu_linux.go / cpu_other.go files.

// cpuSampleWindow is the interval SampleCPU integrates process CPU time over. It
// is short enough not to stall a low-frequency Tick yet long enough that a busy
// process accrues clearly measurable time.
const cpuSampleWindow = 100 * time.Millisecond
