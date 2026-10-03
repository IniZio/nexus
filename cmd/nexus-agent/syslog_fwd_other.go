//go:build !linux

package main

import "context"

func listenDevLog(context.Context, func(string)) error  { return nil }
func listenJournal(context.Context, func(string)) error { return nil }
