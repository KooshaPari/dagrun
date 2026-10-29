//go:build scale

package store

// scaleMeasurementEnabled turns the timing-sensitive scale tests on.
//
// They are off by default: they measure wall time, and a busy machine (or a
// full disk) makes them fail for reasons that say nothing about the tool.
const scaleMeasurementEnabled = true
