package coordinator

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTimezone      = "America/Montreal"
	DefaultDailyTime     = "07:55"
	DefaultWindowStart   = "08:00"
	DefaultWindowEnd     = "18:00"
	DefaultCycleInterval = 45
	ScheduleWeekdays     = "weekdays"
	ScheduleEveryDay     = "every_day"
)

type Config struct {
	AgentProfile         string `json:"agent_profile"`
	MonitoringEnabled    bool   `json:"monitoring_enabled"`
	CycleIntervalMinutes int    `json:"cycle_interval_minutes"`
	ScheduleDays         string `json:"schedule_days"`
	WindowStart          string `json:"monitoring_window_start"`
	WindowEnd            string `json:"monitoring_window_end"`
	DailyReportTime      string `json:"daily_report_time"`
	Timezone             string `json:"timezone"`
	BasePrompt           string `json:"base_prompt"`
	ReportTemplate       string `json:"report_template"`
}

func ConfigFrom(values map[string]any) (Config, error) {
	config := Config{
		MonitoringEnabled:    true,
		CycleIntervalMinutes: DefaultCycleInterval,
		ScheduleDays:         ScheduleWeekdays,
		WindowStart:          DefaultWindowStart,
		WindowEnd:            DefaultWindowEnd,
		DailyReportTime:      DefaultDailyTime,
		Timezone:             DefaultTimezone,
		BasePrompt:           DefaultBasePrompt,
		ReportTemplate:       DefaultReportTemplate,
	}
	if values == nil {
		return config, config.Validate()
	}
	config.AgentProfile = stringValue(values, "agent_profile", config.AgentProfile)
	config.BasePrompt = stringValue(values, "base_prompt", config.BasePrompt)
	config.ReportTemplate = stringValue(values, "report_template", config.ReportTemplate)
	config.Timezone = stringValue(values, "timezone", config.Timezone)
	config.DailyReportTime = stringValue(values, "daily_report_time", config.DailyReportTime)
	config.ScheduleDays = stringValue(values, "schedule_days", config.ScheduleDays)
	config.WindowStart = stringValue(values, "monitoring_window_start", config.WindowStart)
	config.WindowEnd = stringValue(values, "monitoring_window_end", config.WindowEnd)
	if value, ok := values["monitoring_enabled"]; ok {
		parsed, err := boolValue(value)
		if err != nil {
			return Config{}, fmt.Errorf("monitoring_enabled: %w", err)
		}
		config.MonitoringEnabled = parsed
	}
	if value, ok := values["cycle_interval_minutes"]; ok {
		parsed, err := intValue(value)
		if err != nil {
			return Config{}, fmt.Errorf("cycle_interval_minutes: %w", err)
		}
		config.CycleIntervalMinutes = parsed
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if c.CycleIntervalMinutes < 5 || c.CycleIntervalMinutes > 1440 {
		return fmt.Errorf("cycle_interval_minutes must be between 5 and 1440")
	}
	if c.ScheduleDays != ScheduleWeekdays && c.ScheduleDays != ScheduleEveryDay {
		return fmt.Errorf("schedule_days must be weekdays or every_day")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Timezone, err)
	}
	if _, err := parseClock(c.DailyReportTime); err != nil {
		return fmt.Errorf("invalid daily_report_time %q: %w", c.DailyReportTime, err)
	}
	start, err := parseClock(c.WindowStart)
	if err != nil {
		return fmt.Errorf("invalid monitoring_window_start %q: %w", c.WindowStart, err)
	}
	end, err := parseClock(c.WindowEnd)
	if err != nil {
		return fmt.Errorf("invalid monitoring_window_end %q: %w", c.WindowEnd, err)
	}
	if start.Hour() == end.Hour() && start.Minute() == end.Minute() {
		return fmt.Errorf("monitoring window start and end must differ")
	}
	if strings.TrimSpace(c.BasePrompt) == "" {
		return fmt.Errorf("base_prompt is required")
	}
	if strings.TrimSpace(c.ReportTemplate) == "" {
		return fmt.Errorf("report_template is required")
	}
	return nil
}

func (c Config) ReadyForRun() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.AgentProfile) == "" {
		return fmt.Errorf("agent_profile is required")
	}
	return nil
}

func parseClock(value string) (time.Time, error) { return time.Parse("15:04", value) }

func stringValue(values map[string]any, key, fallback string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return fallback
	}
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return fallback
}

func boolValue(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		parsed, err := strconv.ParseBool(typed)
		if err != nil {
			return false, fmt.Errorf("must be a boolean")
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("must be a boolean")
	}
}

func intValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("must be an integer")
		}
		return int(typed), nil
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, fmt.Errorf("must be an integer")
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("must be an integer")
	}
}
