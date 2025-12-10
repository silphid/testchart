package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/spf13/cobra"
	"github.com/yannh/kubeconform/pkg/validator"
	"gopkg.in/yaml.v2"
)

var (
	version       = "v0.0.0"
	saveActual    = false
	showValues    = false
	showAllValues = false
	debugOutput   = ""
)

func main() {
	var testPath string
	var namespace string
	var release string
	var chartVersion string
	var appVersion string
	var concurrency int
	isUpdate := false
	var ignorePatterns []string

	rootCmd := &cobra.Command{
		Use:   "testchart",
		Short: "Tests helm charts",
	}

	rootCmd.PersistentFlags().StringVarP(&testPath, "path", "p", "tests", "Path to tests directory")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "my-namespace", "Name of namespace to use for rendering chart")
	rootCmd.PersistentFlags().StringVarP(&release, "release", "r", "my-release", "Name of release to use for rendering chart")
	rootCmd.PersistentFlags().StringVar(&chartVersion, "chart-version", "", "Version of chart to override for rendering chart")
	rootCmd.PersistentFlags().StringVar(&appVersion, "app-version", "", "App version of chart to override for rendering chart")
	rootCmd.PersistentFlags().BoolVarP(&saveActual, "save-actual", "s", false, "Saves an actual.yaml file in each test dir for troubleshooting")
	rootCmd.PersistentFlags().BoolVarP(&showValues, "show-values", "v", false, "Shows coalesced values for failed tests")
	rootCmd.PersistentFlags().BoolVarP(&showAllValues, "show-all-values", "V", false, "Shows coalesced values for all tests")
	rootCmd.PersistentFlags().StringSliceVarP(&ignorePatterns, "ignore", "i", []string{}, "Regex specifying lines to ignore (can be specified multiple times)")
	rootCmd.PersistentFlags().StringVar(&debugOutput, "debug", "", "location to render failed install output manifests for debugging")
	rootCmd.PersistentFlags().IntVarP(&concurrency, "concurrency", "c", runtime.GOMAXPROCS(0), "test run concurrency")

	runCmd := &cobra.Command{
		Use:   "run [test1 test2 ...]",
		Short: "Run unit tests",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTests(args, testPath, namespace, release, chartVersion, appVersion, isUpdate, ignorePatterns, concurrency)
		},
	}

	updateCmd := &cobra.Command{
		Use:   "update [test1 test2 ...]",
		Short: "Update expected files",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			isUpdate = true
			return runTests(args, testPath, namespace, release, chartVersion, appVersion, isUpdate, ignorePatterns, concurrency)
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

func runTests(args []string, testPath, namespace, releaseName, chartVersion, appVersion string, isUpdate bool, ignorePatterns []string, concurrency int) error {
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		fmt.Println("No tests found")
		return nil
	}

	schema, err := loadCueSchema()
	if err != nil {
		return fmt.Errorf("loading cue schema: %w", err)
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

	suite := NewTestSuite(testNames, isUpdate)

	runOptions := RunOptions{
		RootFS:         testPath,
		IgnorePatterns: ignorePatterns,
		Schema:         schema,
		Concurrency:    concurrency,
		HelmOptions: HelmOptions{
			Namespace:    namespace,
			Release:      releaseName,
			ChartVersion: chartVersion,
			AppVersion:   appVersion,
		},
	}

	start := time.Now()

	if err := suite.Run(runOptions); err != nil {
		return err
	}

	suite.PrintSummary()

	fmt.Println()
	fmt.Println("Tests completed in:", time.Since(start).Round(time.Millisecond).String())

	if !suite.IsSuccessful() {
		os.Exit(1)
	}
	return nil
}

// standardizeTree converts a tree of interface{} to a tree of map[string]interface{}
func standardizeTree(node map[string]any) map[string]any {
	return standardizeNode(node).(map[string]any)
}

func standardizeNode(node any) any {
	switch v := node.(type) {
	case map[any]any:
		newNode := map[string]any{}
		for key, value := range v {
			strKey := key.(string)
			newNode[strKey] = standardizeNode(value)
		}
		return newNode
	case map[string]any:
		for key, value := range v {
			v[key] = standardizeNode(value)
		}
		return v
	case []any:
		for i, elem := range v {
			v[i] = standardizeNode(elem)
		}
		return v
	default:
		return v
	}
}

func loadValuesFile(filePath string) (map[string]any, error) {
	yamlFile, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var data map[string]any
	err = yaml.Unmarshal(yamlFile, &data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func validateManifest(test *Test, manifest string) error {
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
			test.AddValidationError(sig.QualifiedName(), res.Err.Error())
		}
	}

	return nil
}

func removeLinesMatchingPatterns(test *Test, input string, ignorePatterns []*regexp.Regexp) string {
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
		if match {
			test.AddIgnoredLine(line)
		} else {
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

func compareManifests(builder *Test, expectedManifest, actualManifest string, ignoreExpressions []*regexp.Regexp) (bool, error) {
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
			normalizedExpected, err := normalizeYAML(expectedContent)
			if err != nil {
				return false, fmt.Errorf("normalizing expected content: %w:\n%s", err, expectedContent)
			}

			normalizedActual, err := normalizeYAML(actualContent)
			if err != nil {
				return false, fmt.Errorf("normalizing actual content: %w:\n%s", err, actualContent)
			}

			sanitizedExpected := removeLinesMatchingPatterns(builder, normalizedExpected, ignoreExpressions)
			sanitizedActual := removeLinesMatchingPatterns(builder, normalizedActual, ignoreExpressions)

			if sanitizedExpected != sanitizedActual {
				builder.AddDifferentItem(source, normalizedExpected, normalizedActual)
				areEqual = false
			}
			delete(actual, source)
		}
	}

	return areEqual, nil
}

func splitManifest(buffer string) map[string]string {
	items := make(map[string]string)
	delimiter := "---\n# Source: "

	// Split the buffer into chunks using the delimiter
	chunks := strings.SplitSeq(buffer, delimiter)

	// Process each chunk
	for chunk := range chunks {
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

		current, ok := items[sourcePath]
		if ok {
			content = current + "\n---\n" + content
		}
		// Store the content in the map
		items[sourcePath] = content
	}

	return items
}

func loadCueSchema() (*cue.Value, error) {
	data, err := os.ReadFile("./values.cue")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	schema := cuecontext.New().
		CompileBytes(data).
		LookupPath(cue.MakePath(cue.Def("#values")))

	if err := schema.Validate(); err != nil {
		return nil, fmt.Errorf("validating schema: %w", err)
	}

	return &schema, nil
}

func ManyErr[T error](list []T) error {
	errs := make([]error, len(list))
	for i, elem := range list {
		errs[i] = elem
	}
	return errors.Join(errs...)
}

type NopWriterCloser struct {
	io.Writer
}

func (NopWriterCloser) Close() error {
	return nil
}
