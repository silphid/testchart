package main

import (
	"fmt"
	"github.com/imdario/mergo"
	"helm.sh/helm/v3/pkg/cli"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/spf13/cobra"
	"github.com/yannh/kubeconform/pkg/validator"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	helm "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

var version = "v0.0.0"
var isDebug = false

func main() {
	var testPath string
	var namespace string
	var release string
	isUpdate := false
	var ignorePatterns []string

	rootCmd := &cobra.Command{
		Use:   "testchart",
		Short: "Tests helm charts",
	}

	rootCmd.PersistentFlags().StringVarP(&testPath, "path", "p", "tests", "Path to tests directory")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "my-namespace", "Name of namespace to use for rendering chart")
	rootCmd.PersistentFlags().StringVarP(&release, "release", "r", "my-release", "Name of release to use for rendering chart")
	rootCmd.PersistentFlags().BoolVarP(&isDebug, "debug", "d", false, "Enable debug mode")
	rootCmd.PersistentFlags().StringSliceVarP(&ignorePatterns, "ignore", "i", []string{}, "Regex specifying lines to ignore (can be specified multiple times)")

	runCmd := &cobra.Command{
		Use:   "run [test1 test2 ...]",
		Short: "Run unit tests",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTests(args, testPath, namespace, release, isUpdate, ignorePatterns)
		},
	}

	updateCmd := &cobra.Command{
		Use:   "update [test1 test2 ...]",
		Short: "Update expected files",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			isUpdate = true
			return runTests(args, testPath, namespace, release, isUpdate, ignorePatterns)
		},
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Display testchart build version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	}

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(versionCmd)

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runTests(args []string, testPath, namespace, release string, isUpdate bool, ignorePatterns []string) error {
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		fmt.Println("No tests found")
		return nil
	}

	var testNames []string
	if len(args) > 0 {
		testNames = args
	} else {
		files, err := os.ReadDir(testPath)
		if err != nil {
			log.Fatal(err)
		}

		for _, file := range files {
			if file.IsDir() {
				testNames = append(testNames, file.Name())
			}
		}
	}

	// Load chart
	chartPath, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	chart, err := loader.Load(chartPath)
	if err != nil {
		return err
	}

	areAllSuccess := true
	for _, testName := range testNames {
		fmt.Println("========================================")
		fmt.Printf("🧪 %s\n", testName)

		isSuccess, err := runTest(chart, namespace, release, testPath, testName, isUpdate, ignorePatterns)
		if err != nil {
			return fmt.Errorf("running test %s: %w", testName, err)
		}
		areAllSuccess = areAllSuccess && isSuccess
		if isUpdate {
			if isSuccess {
				fmt.Println("👍 Nothing to update in expected file")
			} else {
				fmt.Println("📝 Updated expected file")
			}
		} else {
			if isSuccess {
				fmt.Println("✅  Passed")
			} else {
				fmt.Println("❌  Failed")
			}
		}
	}

	fmt.Println("========================================")
	if isUpdate {
		if areAllSuccess {
			fmt.Println("👍 Nothing to update in any expected file")
		} else {
			fmt.Println("📝 Updated some expected files")
		}
	} else {
		if areAllSuccess {
			fmt.Println("✅  All tests succeeded!")
		} else {
			fmt.Println("❌  Some tests failed")
		}
	}

	return nil
}

func runTest(chart *helm.Chart, namespace, releaseName, testPath, testName string, isUpdate bool, ignorePatterns []string) (bool, error) {
	// Create action config
	settings := cli.New()
	actionConfig := new(action.Configuration)
	debugLog := func(format string, v ...interface{}) {
		fmt.Printf(format, v)
	}
	if !isDebug {
		debugLog = nil
	}
	err := actionConfig.Init(settings.RESTClientGetter(), settings.Namespace(), "memory", debugLog)
	if err != nil {
		log.Fatal(err)
	}

	// Create install action
	installAction := action.NewInstall(actionConfig)
	installAction.Namespace = namespace
	installAction.ReleaseName = releaseName
	installAction.IncludeCRDs = true
	installAction.ClientOnly = true

	// Load chart's default values file, if any
	defaultValuesPath := "values.yaml"
	var defaultValues map[string]interface{}
	if _, err := os.Stat(defaultValuesPath); !os.IsNotExist(err) {
		debug("Using chart default values file as base")
		defaultValues, err = loadValuesFile(defaultValuesPath)
		if err != nil {
			return false, fmt.Errorf("parsing default values file %q: %w", defaultValuesPath, err)
		}
	} else {
		debug("No chart default values file found")
	}

	// Load test values file
	testValuesPath := filepath.Join(testPath, testName, "values.yaml")
	testValues, err := loadValuesFile(testValuesPath)
	if err != nil {
		return false, fmt.Errorf("parsing test values file %q: %w", testValuesPath, err)
	}

	// Merge values
	values := defaultValues
	err = mergo.Merge(&values, testValues)
	if err != nil {
		return false, fmt.Errorf("merging test values onto chart default values: %w", err)
	}
	values = standardizeTree(values).(map[string]interface{})
	if isDebug {
		valuesYaml, err := yaml.Marshal(values)
		if err != nil {
			return false, fmt.Errorf("serializing values to yaml: %w", err)
		}
		fmt.Println("📜 Values:")
		fmt.Println(string(valuesYaml))
		fmt.Println("-----------")
	}

	// Render chart templates
	release, err := installAction.Run(chart, values)
	if err != nil {
		return false, err
	}
	actualStr := release.Manifest

	// Write actual.yaml in debug mode
	if isDebug {
		actualPath := filepath.Join(testPath, testName, "actual.yaml")
		err := os.WriteFile(actualPath, []byte(actualStr), 0644)
		if err != nil {
			return false, fmt.Errorf("writing actual.yaml file for debug purposes: %w", err)
		}
	}

	// Read expected.yaml
	expectedPath := filepath.Join(testPath, testName, "expected.yaml")
	expectedBytes, err := os.ReadFile(expectedPath)
	if err != nil {
		return false, fmt.Errorf("reading expected.yaml file: %w", err)
	}
	expectedStr := string(expectedBytes)

	// Compare
	areEqual, err := compareExpectedAndActualYAML(expectedStr, actualStr, ignorePatterns)
	if err != nil {
		return false, err
	}

	// Validate
	isValid, err := validateManifest(release.Manifest)
	if err != nil {
		return false, err
	}

	// Update expected?
	if isUpdate && !areEqual {
		err := os.WriteFile(expectedPath, []byte(actualStr), 0644)
		if err != nil {
			return false, err
		}
	}

	return areEqual && isValid, nil
}

// standardizeTree converts a tree of interface{} to a tree of map[string]interface{}
func standardizeTree(node interface{}) interface{} {
	switch v := node.(type) {
	case map[interface{}]interface{}:
		newNode := make(map[string]interface{})
		for key, value := range v {
			strKey := key.(string)
			newNode[strKey] = standardizeTree(value)
		}
		return newNode
	case map[string]interface{}:
		for key, value := range v {
			v[key] = standardizeTree(value)
		}
		return v
	case []interface{}:
		for i, elem := range v {
			v[i] = standardizeTree(elem)
		}
		return v
	default:
		return v
	}
}

func loadValuesFile(filePath string) (map[string]interface{}, error) {
	yamlFile, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var data map[string]interface{}
	err = yaml.Unmarshal(yamlFile, &data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func validateManifest(manifest string) (bool, error) {
	v, err := validator.New(nil, validator.Opts{Strict: true, IgnoreMissingSchemas: true})
	if err != nil {
		return false, fmt.Errorf("initializing validator: %w", err)
	}

	readCloser := io.NopCloser(strings.NewReader(manifest))
	filePath := "rendered.yaml"
	isValid := true
	for i, res := range v.Validate(filePath, readCloser) { // A file might contain multiple resources
		// File starts with ---, the parser assumes a first empty resource
		if res.Status == validator.Invalid || res.Status == validator.Error {
			sig, err := res.Resource.Signature()
			if err != nil {
				return false, fmt.Errorf("creating signature for invalid resource #%d: %w", i, err)
			}
			fmt.Printf("Invalid resource %s: %s\n", sig.QualifiedName(), res.Err)
			isValid = false
		}
	}

	return isValid, nil
}

func compareExpectedAndActualYAML(expectedStr, actualStr string, ignoreExpressions []string) (bool, error) {
	ignorePatterns, err := compileIgnorePatterns(ignoreExpressions)
	if err != nil {
		return false, err
	}

	expectedStr = removeLinesMatchingPatterns(expectedStr, ignorePatterns)
	actualStr = removeLinesMatchingPatterns(actualStr, ignorePatterns)

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(expectedStr, actualStr, false)

	// Check if the strings are the same or different
	areEqual := true
	for _, diff := range diffs {
		if diff.Type != diffmatchpatch.DiffEqual {
			areEqual = false
			break
		}
	}

	if !areEqual {
		diffOutput := dmp.DiffPrettyText(diffs)
		fmt.Println("💔 Diff:")
		fmt.Println(diffOutput)
	}

	return areEqual, nil
}

func removeLinesMatchingPatterns(input string, ignorePatterns []*regexp.Regexp) string {
	lines := strings.Split(input, "\n")
	var filteredLines []string
	for _, line := range lines {
		match := false
		for _, pattern := range ignorePatterns {
			if pattern.MatchString(line) {
				match = true
				break
			}
		}
		if !match {
			filteredLines = append(filteredLines, line)
		}
	}
	return strings.Join(filteredLines, "\n")
}

func compileIgnorePatterns(ignoreExpressions []string) ([]*regexp.Regexp, error) {
	var ignorePatterns []*regexp.Regexp
	for _, expr := range ignoreExpressions {
		pattern, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("failed to compile ignore pattern %q: %v", expr, err)
		}
		ignorePatterns = append(ignorePatterns, pattern)
	}
	return ignorePatterns, nil
}

func debug(format string, a ...interface{}) {
	if isDebug {
		fmt.Printf(format+"\n", a...)
	}
}
