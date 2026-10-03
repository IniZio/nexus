package hubstate

import "errors"

// Darwin stubs: hub coordination is deferred on macOS.

func BootID() (string, error) { return "", ErrUnsupported }

func ProcOf(pid int) (Proc, error) { return Proc{}, ErrUnsupported }

func Alive(p Proc) bool { return false }

func realStatfsType(path string) (int64, error) { return 0, errors.New("hubstate: statfs unsupported") }
