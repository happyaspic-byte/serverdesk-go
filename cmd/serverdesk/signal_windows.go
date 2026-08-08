//go:build windows

package main

import (
	"os"
	"os/signal"
)

// Windows 는 SIGTERM 이 없다 — Ctrl+C/콘솔 닫기(os.Interrupt 계열)만 받는다.
func notifyStop(ch chan<- os.Signal) { signal.Notify(ch, os.Interrupt) }
