package main

import (
	"fmt"
	"github.com/containerd/containerd/pkg/cri/util"
	"github.com/imdario/mergo"
	"helm.sh/helm/v3/pkg/cli"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yannh/kubeconform/pkg/validator"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	helm "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

var version = "v0.0.0"
var saveActual = false
var showValues = false

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
	rootCmd.PersistentFlags().BoolVarP(&saveActual, "save-actual", "s", false, "Saves an actual.yaml file in each test dir for troubleshooting")
	rootCmd.PersistentFlags().BoolVarP(&showValues, "show-values", "v", false, "Shows coalesced values used for rendering chart")
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

	builder := NewPrintBuilder(isUpdate)
	builder.StartAllTests()

	for _, testName := range testNames {
		err := runTest(builder, chart, namespace, release, testPath, testName, isUpdate, ignorePatterns)
		if err != nil {
			return fmt.Errorf("running test %s: %w", testName, err)
		}
	}

	builder.EndAllTests()
	if !builder.IsSuccessful() {
		os.Exit(1)
	}
	return nil
}

func runTest(builder Builder, chart *helm.Chart, namespace, releaseName, testPath, testName string, isUpdate bool, ignorePatterns []string) error {
	builder.StartTest(testName)

	// Create action config
	settings := cli.New()
	actionConfig := new(action.Configuration)
	err := actionConfig.Init(settings.RESTClientGetter(), namespace, "memory", nil)
	if err != nil {
		log.Fatal(err)
	}

	// Create install action
	installAction := action.NewInstall(actionConfig)
	installAction.Namespace = namespace
	installAction.ReleaseName = releaseName
	installAction.IncludeCRDs = true
	installAction.ClientOnly = true

	// Load test values file
	testValuesPath := filepath.Join(testPath, testName, "values.yaml")
	testValues, err := loadValuesFile(testValuesPath)
	if err != nil {
		return fmt.Errorf("parsing test values file %q: %w", testValuesPath, err)
	}

	// Coalesce test values onto chart default values
	var values map[string]interface{}
	err = util.DeepCopy(&values, chart.Values)
	if err != nil {
		return fmt.Errorf("copying chart default values: %w", err)
	}
	err = mergo.Merge(&values, testValues, mergo.WithOverride)
	if err != nil {
		return fmt.Errorf("merging test values onto chart default values: %w", err)
	}
	values = standardizeTree(values).(map[string]interface{})
	if showValues {
		builder.ShowValues(values)
	}

	// Render chart templates
	release, err := installAction.Run(chart, values)
	if err != nil {
		return err
	}
	actualManifest := release.Manifest

	// Save actual.yaml for troubleshooting purposes
	if saveActual {
		actualPath := filepath.Join(testPath, testName, "actual.yaml")
		err := os.WriteFile(actualPath, []byte(actualManifest), 0644)
		if err != nil {
			return fmt.Errorf("writing actual.yaml file for debug purposes: %w", err)
		}
	}

	// Read expected.yaml
	expectedPath := filepath.Join(testPath, testName, "expected.yaml")
	expectedBytes, err := os.ReadFile(expectedPath)
	if err != nil {
		return fmt.Errorf("reading expected.yaml file: %w", err)
	}
	expectedManifest := string(expectedBytes)

	// Filter manifests for ignored patterns
	ignoreExpressions, err := compileIgnorePatterns(ignorePatterns)
	if err != nil {
		return fmt.Errorf("compiling ignore patterns: %w", err)
	}
	actualManifest = removeLinesMatchingPatterns(actualManifest, ignoreExpressions)
	expectedManifest = removeLinesMatchingPatterns(expectedManifest, ignoreExpressions)

	// Compare
	isEqual := compareManifests(builder, expectedManifest, actualManifest)
	builder.SetTestComparisonResult(isEqual)

	// Update expected?
	if isUpdate {
		if !isEqual {
			err := os.WriteFile(expectedPath, []byte(actualManifest), 0644)
			if err != nil {
				return fmt.Errorf("writing updated expected.yaml file: %w", err)
			}
		}
	}

	// Validate
	err = validateManifest(builder, release.Manifest)
	if err != nil {
		return fmt.Errorf("validating manifest: %w", err)
	}

	return builder.EndTest()
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

func validateManifest(builder Builder, manifest string) error {
	v, err := validator.New(nil, validator.Opts{Strict: true, IgnoreMissingSchemas: true})
	if err != nil {
		return fmt.Errorf("initializing validator: %w", err)
	}

	readCloser := io.NopCloser(strings.NewReader(manifest))
	filePath := "rendered.yaml"
	for i, res := range v.Validate(filePath, readCloser) { // A file might contain multiple resources
		// File starts with ---, the parser assumes a first empty resource
		if res.Status == validator.Invalid || res.Status == validator.Error {
			sig, err := res.Resource.Signature()
			if err != nil {
				return fmt.Errorf("creating signature for invalid resource #%d: %w", i, err)
			}
			builder.AddValidationError(sig.QualifiedName(), res.Err.Error())
		}
	}

	return nil
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

func compareManifests(builder Builder, expectedManifest, actualManifest string) bool {
	expected := splitManifest(expectedManifest)
	actual := splitManifest(actualManifest)
	areEqual := true

	// Find missing items
	for source, expectedContent := range expected {
		if _, ok := actual[source]; !ok {
			builder.AddMissingItem(source, expectedContent)
			delete(expected, source)
			areEqual = false
		}
	}

	// Find extra items
	for source, actualContent := range actual {
		if _, ok := expected[source]; !ok {
			builder.AddExtraItem(source, actualContent)
			delete(actual, source)
			areEqual = false
		}
	}

	// Find different items
	for source, expectedContent := range expected {
		if actualContent, ok := actual[source]; ok {
			if expectedContent != actualContent {
				builder.AddDifferentItem(source, expectedContent, actualContent)
				areEqual = false
			}
			delete(actual, source)
		}
	}

	return areEqual
}

func splitManifest(buffer string) map[string]string {
	items := make(map[string]string)
	delimiter := "---\n# Source: "

	// Split the buffer into chunks using the delimiter
	chunks := strings.Split(buffer, delimiter)

	// Process each chunk
	for _, chunk := range chunks {
		// Remove leading and trailing whitespaces
		chunk = strings.TrimSpace(chunk)

		// Skip empty chunks
		if chunk == "" {
			continue
		}

		// Find the source path and content within the chunk
		parts := strings.SplitN(chunk, "\n", 2)
		if len(parts) != 2 {
			continue
		}

		// Extract the source path and content
		sourcePath := strings.TrimSpace(parts[0])
		content := strings.TrimSpace(parts[1])

		// Store the content in the map
		items[sourcePath] = content
	}

	return items
}
