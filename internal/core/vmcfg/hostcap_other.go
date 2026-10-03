//go:build !darwin

package vmcfg

func darwinMemMiB() uint64 { return 0 }
