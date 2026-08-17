package main

import (
	"fmt"
	"time"
)

const (
	defaultTimezone      = "America/Montreal"
	defaultStandupTime   = "07:55"
	defaultCycleInterval = 45
)

type Config struct {
	AgentProfile         string `json:"agent_profile"`
	BasePrompt           string `json:"base_prompt"`
	Timezone             string `json:"timezone"`
	StandupTime          string `json:"standup_time"`
	CycleIntervalMinutes int    `json:"cycle_interval_minutes"`
}

func configFrom(values map[string]any) (Config, error) {
	config := Config{Timezone: defaultTimezone, StandupTime: defaultStandupTime, CycleIntervalMinutes: defaultCycleInterval}
	if value, ok := values["agent_profile"].(string); ok {
		config.AgentProfile = value
	}
	if value, ok := values["base_prompt"].(string); ok {
		config.BasePrompt = value
	}
	if value, ok := values["timezone"].(string); ok && value != "" {
		config.Timezone = value
	}
	if value, ok := values["standup_time"].(string); ok && value != "" {
		config.StandupTime = value
	}
	if value, ok := values["cycle_interval_minutes"].(float64); ok && value != 0 {
		config.CycleIntervalMinutes = int(value)
	}
	if _, err := time.LoadLocation(config.Timezone); err != nil {
		return Config{}, fmt.Errorf("invalid timezone %q: %w", config.Timezone, err)
	}
	if _, err := time.Parse("15:04", config.StandupTime); err != nil {
		return Config{}, fmt.Errorf("invalid standup_time %q: %w", config.StandupTime, err)
	}
	if config.CycleIntervalMinutes < 15 || config.CycleIntervalMinutes > 240 {
		return Config{}, fmt.Errorf("cycle_interval_minutes must be between 15 and 240")
	}
	return config, nil
}

func nextStandup(now time.Time, config Config) (time.Time, error) {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	clock, err := time.Parse("15:04", config.StandupTime)
	if err != nil {
		return time.Time{}, err
	}
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	for next.Weekday() == time.Saturday || next.Weekday() == time.Sunday {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}
