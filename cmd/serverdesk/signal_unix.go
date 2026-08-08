//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyStop 은 SIGTERM/SIGINT 를 종료 채널에 연결한다(Unix).
func notifyStop(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT) }
