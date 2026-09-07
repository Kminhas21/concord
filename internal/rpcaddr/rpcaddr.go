// Package rpcaddr holds the default daemon address shared by the concord daemon
// and its hook client, so the RPC endpoint is defined in exactly one place.
package rpcaddr

// Default is the loopback address the daemon listens on, and the address the
// hook client dials, when CONCORD_ADDR is unset. Loopback only: concord is one
// daemon per machine, never networked.
const Default = "127.0.0.1:8973"
