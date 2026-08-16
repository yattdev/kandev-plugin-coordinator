package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigFromUsesDefaults(t *testing.T) {
	config, err := configFrom(nil)
	require.NoError(t, err)
	require.Equal(t, defaultTimezone, config.Timezone)
	require.Equal(t, defaultStandupTime, config.StandupTime)
	require.Equal(t, defaultCycleInterval, config.CycleIntervalMinutes)
}

func TestNextStandupSkipsWeekend(t *testing.T) {
	config := Config{Timezone: "America/Montreal", StandupTime: "07:55"}
	next, err := nextStandup(time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC), config)
	require.NoError(t, err)
	require.Equal(t, time.Monday, next.Weekday())
	require.Equal(t, 17, next.Day())
}

func TestComposePromptAppendsSafetyInvariants(t *testing.T) {
	prompt := composePrompt("base", "workstep")
	require.Contains(t, prompt, "base")
	require.Contains(t, prompt, "workstep")
	require.Contains(t, prompt, "NON-OVERRIDABLE SAFETY INVARIANTS")
}
