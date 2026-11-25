//go:build !windows

package engine

// Payload is empty outside Windows; pass --engine to use a local build.
var Payload []byte
