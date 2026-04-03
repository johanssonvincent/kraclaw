//go:build tools
// +build tools

package main

import (
	_ "github.com/nats-io/nats.go"
	_ "github.com/nats-io/nats-server/v2"
)
