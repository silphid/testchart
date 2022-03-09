package internal

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
)

const TestsDir = "tests"
const ExpectedFileName = "expected.yaml"
const ActualFileName = "actual.yaml"
const ValuesFileName = "values.yaml"

func Run(config *Config) error {
	Log("Updating helm dependencies")
	err := exec.Command("helm", "dependency", "update").Run()
	if err != nil {
		return fmt.Errorf("updating helm dependencies: %w", err)
	}

	Log("Running tests")
	files, err := ioutil.ReadDir(TestsDir)
	if err != nil {
		return fmt.Errorf("listing directory content for %q: %w", TestsDir, err)
	}
	for _, f := range files {
		if f.IsDir() {
			err = runTest(config, f.Name())
			if err != nil {
				return fmt.Errorf("running test in dir %q: %w", f.Name(), err)
			}
		}
	}

	return nil
}

func runTest(config *Config, testName string) error {
	fmt.Printf("========================================\n"+
		"TEST: %s\n"+
		"----------------------------------------\n", testName)

	testDir := filepath.Join(TestsDir, testName)
	// expectedFilePath := filepath.Join(testDir, ExpectedFileName)
	actualFilePath := filepath.Join(testDir, ActualFileName)
	valuesFilePath := filepath.Join(testDir, ValuesFileName)

	err := render(config, testName, actualFilePath, valuesFilePath)
	if err != nil {
		return fmt.Errorf("rendering template for test %q: %w", testName, err)
	}

	if !config.Debug {
		os.Remove(actualFilePath)
	}

	return nil
}

func render(config *Config, testName, targetFilePath, valuesFilePath string) error {
	// Create empty target file
	targetFile, err := os.Create(targetFilePath)
	if err != nil {
		return fmt.Errorf("creating empty rendering target file %q: %w", targetFilePath, err)
	}
	defer targetFile.Close()

	// Run helm template
	Log("Rendering helm template")
	args := []string{
		"template",
		config.Release,
		".",
		"-f",
		valuesFilePath,
	}
	if config.Debug {
		args = append(args, "--debug")
	}
	cmd := exec.Command("helm", args...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = targetFile
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("rendering helm template for test %q: %w", testName, err)
	}

	return nil
}
