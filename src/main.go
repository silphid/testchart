package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"

	"github.com/spf13/cobra"
	"github.com/yannh/kubeconform/pkg/validator"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
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

	runCmd := &cobra.Command{
		Use:   "run [test1 test2 ...]",
		Short: "Run unit tests",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTests(args, testPath, namespace, release, chartVersion, appVersion, isUpdate, ignorePatterns)
		},
	}

	updateCmd := &cobra.Command{
		Use:   "update [test1 test2 ...]",
		Short: "Update expected files",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			isUpdate = true
			return runTests(args, testPath, namespace, release, chartVersion, appVersion, isUpdate, ignorePatterns)
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

func runTests(args []string, testPath, namespace, releaseName, chartVersion, appVersion string, isUpdate bool, ignorePatterns []string) error {
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

	builder := NewPrintBuilder(isUpdate)
	builder.StartAllTests(testNames)

	// Create action config
	settings := cli.New()
	actionConfig := new(action.Configuration)

	if err := actionConfig.Init(settings.RESTClientGetter(), namespace, "memory", nil); err != nil {
		log.Fatal(err)
	}

	// Create install action
	installAction := action.NewInstall(actionConfig)
	installAction.Namespace = namespace
	installAction.ReleaseName = releaseName
	installAction.DryRun = true
	installAction.IncludeCRDs = true
	installAction.ClientOnly = true
	installAction.Replace = true

	// Load chart
	chartPath, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("getting chart path: %w", err)
	}
	theChart, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("loading chart: %w", err)
	}

	// Optionally override chart and app versions
	if chartVersion != "" {
		theChart.Metadata.Version = chartVersion
	}
	if appVersion != "" {
		theChart.Metadata.AppVersion = appVersion
	}

	for _, testName := range testNames {
		err := runTest(builder, theChart, installAction, testPath, testName, isUpdate, ignorePatterns, schema)
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

func runTest(builder Builder, theChart *chart.Chart, installAction *action.Install, testPath, testName string, isUpdate bool, ignorePatterns []string, schema *cue.Value) error {
	builder.StartTest(testName)

	// Load test values file
	testValuesPath := filepath.Join(testPath, testName, "values.yaml")
	testValues, err := loadValuesFile(testValuesPath)
	if err != nil {
		return fmt.Errorf("parsing test values file %q: %w", testValuesPath, err)
	}

	testValues = standardizeTree(testValues)

	if schema != nil {
		if err := schema.Unify(schema.Context().Encode(testValues)).Decode(&testValues); err != nil {
			return fmt.Errorf("unifying values.yaml with schema:\n%w\n\n", ManyErr(cueerrors.Errors(err)))
		}
	}

	// Show coalesced values
	builder.ShowValues(func() (string, error) {
		values, err := chartutil.ToRenderValues(theChart, testValues, chartutil.ReleaseOptions{Name: installAction.ReleaseName, Namespace: installAction.Namespace}, nil)
		if err != nil {
			return "", fmt.Errorf("coalescing test values onto chart default values: %w", err)
		}
		values = values["Values"].(chartutil.Values)
		valuesYaml, err := yaml.Marshal(values)
		if err != nil {
			return "", fmt.Errorf("serializing values to yaml: %w", err)
		}
		return strings.TrimSpace(string(valuesYaml)), nil
	})

	// Render chart templates
	release, err := installAction.Run(theChart, testValues)
	if debugOutput != "" {
		file, err := func() (io.WriteCloser, error) {
			if debugOutput == "-" {
				return NopWriterCloser{os.Stderr}, nil
			}
			return os.Create(debugOutput)
		}()
		if err == nil {
			_, _ = file.Write([]byte(release.Manifest))
			_ = file.Close()
		}
	}
	if err != nil {
		return err
	}

	// Combine regular manifests and hook manifests
	var manifests bytes.Buffer
	_, _ = fmt.Fprintln(&manifests, strings.TrimSpace(release.Manifest))
	for _, m := range release.Hooks {
		_, _ = fmt.Fprintf(&manifests, "---\n# Source: %s\n%s\n", m.Path, m.Manifest)
	}

	// Save actual.yaml for troubleshooting purposes
	if saveActual {
		actualPath := filepath.Join(testPath, testName, "actual.yaml")
		err := os.WriteFile(actualPath, manifests.Bytes(), 0o644)
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
	actualManifest := removeLinesMatchingPatterns(manifests.String(), ignoreExpressions)
	expectedManifest = removeLinesMatchingPatterns(expectedManifest, ignoreExpressions)

	// Compare
	isEqual := compareManifests(builder, expectedManifest, actualManifest)
	builder.SetTestComparisonResult(isEqual)

	// Update expected?
	if isUpdate {
		// Normalize the actual content for potential writing
		normalizedActualManifest, err := normalizeManifest(actualManifest)
		if err != nil {
			// Fall back to original content if normalization fails
			normalizedActualManifest = actualManifest
		}

		// Check if we need to update due to semantic differences
		hasSemanticChanges := !isEqual

		// Check if we need to update due to formatting differences
		hasFormattingChanges := expectedManifest != normalizedActualManifest

		if hasSemanticChanges || hasFormattingChanges {
			err = os.WriteFile(expectedPath, []byte(normalizedActualManifest), 0o644)
			if err != nil {
				return fmt.Errorf("writing updated expected.yaml file: %w", err)
			}

			// Set update type for builder reporting
			if hasSemanticChanges {
				builder.SetUpdateType("semantic")
			} else {
				builder.SetUpdateType("formatting")
			}
		} else {
			builder.SetUpdateType("none")
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
func standardizeTree(node map[string]interface{}) map[string]interface{} {
	return standardizeNode(node).(map[string]interface{})
}

func standardizeNode(node interface{}) interface{} {
	switch v := node.(type) {
	case map[interface{}]interface{}:
		newNode := map[string]interface{}{}
		for key, value := range v {
			strKey := key.(string)
			newNode[strKey] = standardizeNode(value)
		}
		return newNode
	case map[string]interface{}:
		for key, value := range v {
			v[key] = standardizeNode(value)
		}
		return v
	case []interface{}:
		for i, elem := range v {
			v[i] = standardizeNode(elem)
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
			// Normalize both before comparison
			normalizedExpected, err1 := normalizeYAML(expectedContent)
			normalizedActual, err2 := normalizeYAML(actualContent)

			// Fall back to original comparison if normalization fails
			if err1 != nil || err2 != nil {
				if expectedContent != actualContent {
					builder.AddDifferentItem(source, expectedContent, actualContent)
					areEqual = false
				}
			} else if normalizedExpected != normalizedActual {
				// Use normalized content for diff display
				builder.AddDifferentItem(source, normalizedExpected, normalizedActual)
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
