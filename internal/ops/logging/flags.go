package logging

import "os"

// FlagsFromEnv resolves NAVI_LOG_LEVEL and returns the level and log
// file path from the environment (spec: env NAVI_LOG_LEVEL, default
// info; stderr default sink).
func LevelFromEnv() Level {
	return ParseLevel(os.Getenv("NAVI_LOG_LEVEL"))
}

// FileFromEnv returns the NAVI_LOG_FILE path ("" = stderr).
func FileFromEnv() string {
	return os.Getenv("NAVI_LOG_FILE")
}
