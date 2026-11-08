//go:build !e2e_forward

package main

import "github.com/apohor/rivolt/internal/rivian"

// rivianEndpointOverride is the default build's answer: there is none. A release binary or image never reads RIVOLT_RIVIAN_ENDPOINT, so no
// environment of a deployed server can point users' Rivian tokens at another host. The multi-pod forward e2e harness builds its own binary with
// -tags e2e_forward; see rivian_endpoint_e2e.go.
func rivianEndpointOverride() (func(*rivian.LiveClient) *rivian.LiveClient, error) { return nil, nil }
