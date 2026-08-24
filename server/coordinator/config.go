package coordinator

import (
	"fmt"
	"strings"
)

type Config struct {
	AgentProfile   string `json:"agent_profile"`
	BasePrompt     string `json:"base_prompt"`
	ReportTemplate string `json:"report_template"`
}

func ConfigFrom(values map[string]any) (Config, error) {
	config := Config{
		BasePrompt:     DefaultBasePrompt,
		ReportTemplate: DefaultReportTemplate,
	}
	if values == nil {
		return config, config.Validate()
	}
	config.AgentProfile = stringValue(values, "agent_profile", config.AgentProfile)
	config.BasePrompt = stringValue(values, "base_prompt", config.BasePrompt)
	config.ReportTemplate = stringValue(values, "report_template", config.ReportTemplate)
	return config, config.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.BasePrompt) == "" {
		return fmt.Errorf("base_prompt is required")
	}
	if strings.TrimSpace(c.ReportTemplate) == "" {
		return fmt.Errorf("report_template is required")
	}
	return nil
}

func (c Config) ReadyForRun() error {
	return c.Validate()
}

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
