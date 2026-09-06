package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadDefinition parses a navi definition YAML file and validates it.
func LoadDefinition(path string) (*NaviDefinition, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read definition %s: %w", path, err)
	}
	return ParseDefinition(blob)
}

// ParseDefinition parses and validates a navi definition from YAML.
func ParseDefinition(blob []byte) (*NaviDefinition, error) {
	var d NaviDefinition
	if err := yaml.Unmarshal(blob, &d); err != nil {
		return nil, fmt.Errorf("config: parse definition: %w", err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}
