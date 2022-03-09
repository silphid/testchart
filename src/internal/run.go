package internal

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	// "os"
)

func Run(config *Config) error {
	fmt.Printf("Version: %d\n", config.Version)

	testsDir := "tests"
	files, err := ioutil.ReadDir(testsDir)
	if err != nil {
		return fmt.Errorf("listing directory content for %q: %w", testsDir, err)
	}

	for _, f := range files {
		if f.IsDir() {
			testDir := filepath.Join(testsDir, f.Name())
			err = run(config, testDir)
			if err != nil {
				return fmt.Errorf("running test in dir %q: %w", f.Name(), err)
			}
		}
	}

	return nil
}

func run(config *Config, dir string) error {
	Log("Running test for dir %q", dir)
	return nil
}
