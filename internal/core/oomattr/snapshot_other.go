//go:build !linux

package oomattr

func takeFrom(string) Snapshot { return Snapshot{} }
