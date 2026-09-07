package logging

import (
	"os"
)

// WireFromEnv builds a Logger from the environment (NAVI_LOG_LEVEL,
// NAVI_LOG_FILE). Returns a stderr logger at info when unset. Used by
// cmd/navi and the daemon startup chain.
func WireFromEnv() *Logger {
	level := LevelFromEnv()
	logFile := FileFromEnv()
	if logFile == "" {
		return New(os.Stderr, level, nil)
	}
	f, ferr := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if ferr != nil {
		// Degrade to stderr if the file can't be opened.
		return New(os.Stderr, level, nil)
	}
	return New(f, level, nil)
}
