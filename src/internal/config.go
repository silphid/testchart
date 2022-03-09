package internal

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	currentVersion = 1
)

// Kubeval controls the execution of the kubeval command, which validates generated
// yaml outputs against their expected schemas.
type KubevalConfig struct {
	// Enable is a boolean flag determining whether to perform yaml schema validation or not using kubeval.
	Enable bool `yaml:"enable"`
	// Arguments passed to kubeval command.
	KubeValArgs []string `yaml:"kubevalArgs"`
}

// Config represents configuration loaded from yaml file and command line arguments.
type Config struct {
	// Version specifies the version of the file format for future evolution.
	Version int `yaml:"version"`
	// Kubeval controls the execution of the kubeval command, which validates generated
	// yaml outputs against their expected schemas.
	Kubeval KubevalConfig `yaml:"kubeval"`
	// Update controls whether to overwrite expected files
	Update bool `yaml:"-"`
	// Debug controls whether to run in debug mode, in which actual rendered files
	// get generated in each test folder for inspection and the helm command outputs
	// verbose messages. Defaults to false.
	Debug bool `yaml:"-"`
	// Release is the name of the release to use while rendering templates.
	// Defaults to "release123".
	Release string `yaml:"release"`
	// List of regular expressions describing lines to exclude from expected and actual files.
	// All lines for which any of the expressions matches get ignored entirely.
	IgnoreLines []string `yaml:"ignoreLines"`
}

// readContextFileFromHomeDirectory looks in home directory for a .yeyrc.yaml file and returns
// the bytes in the file, the absolute path to contextFile and an error if encountered.
// If none is found it climbs the directory hierarchy.
func LoadConfig(config *Config) error {
	// Set default values
	config.Version = 1
	config.Release = "release123"

	// Load file data
	data, err := os.ReadFile("tests/tests.yaml")
	if err != nil {
		return err
	}

	// Parse data as yaml
	if err := yaml.Unmarshal(data, config); err != nil {
		return fmt.Errorf("failed to unmarshal config file: %w", err)
	}

	// Ensure compatible version
	if config.Version != currentVersion {
		return fmt.Errorf("unsupported config file version")
	}

	return nil
}
