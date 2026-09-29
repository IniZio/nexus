package sprites

import (
	"fmt"

	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
)

func init() {
	registry.Register(registry.Sprites, func(cfg any) (driver.Driver, error) {
		switch c := cfg.(type) {
		case nil:
			return New(Config{})
		case Config:
			return New(c)
		case *Config:
			if c == nil {
				return New(Config{})
			}
			return New(*c)
		default:
			return nil, fmt.Errorf("sprites: config must be sprites.Config, got %T", cfg)
		}
	})
}
