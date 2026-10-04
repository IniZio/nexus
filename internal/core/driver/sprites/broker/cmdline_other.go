//go:build !linux && !darwin

package broker

import "errors"

func processCmdline(int) ([]string, error) {
	return nil, errors.New("broker: process identity unsupported on this platform")
}
