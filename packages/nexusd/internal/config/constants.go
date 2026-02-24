package config

import (
	"time"
)

const (
	DefaultPortRangeStart = 32800
	DefaultPortRangeEnd   = 34999

	DefaultIdleTimeout = 30 * time.Second
	DefaultStopTimeout = 30 * time.Second
)
