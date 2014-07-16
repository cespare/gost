// Package llog implements a simple logger on top of stdlib's log with two log levels.
package llog

import (
	"log"
)

type Logger struct {
	*log.Logger
	dbg bool
}

func NewLogger(logger *log.Logger, debug bool) *Logger {
	return &Logger{logger, debug}
}

func (l *Logger) Debugf(format string, args ...interface{}) {
	if l.dbg {
		log.Printf(format, args...)
	}
}

func (l *Logger) Debugln(args ...interface{}) {
	if l.dbg {
		log.Println(args...)
	}
}
