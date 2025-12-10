package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	cueerrors "cuelang.org/go/cue/errors"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
)

type Item struct {
	source, expected, actual string
}

type ValidationError struct {
	signature, error string
}

type Test struct {
	name                                     string
	isUpdate                                 bool
	updateType                               string
	isSame, isValid                          bool
	differentItems, missingItems, extraItems []Item
	validationErrors                         []ValidationError
	getValuesYaml                            func() (string, error)
	// updateCounts                             map[string]int // Track update types: "none", "formatting", "semantic"
	// longestName  int
	ignoredLines []string
}

func (test *Test) Run(theChart *chart.Chart, installAction *action.Install, rootPath string, ignorePatterns []string, schema *cue.Value) error {
	// Load test values file
	testValuesPath := filepath.Join(rootPath, test.name, "values.yaml")
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
	test.ShowValues(func() (string, error) {
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
		actualPath := filepath.Join(rootPath, test.name, "actual.yaml")
		err := os.WriteFile(actualPath, manifests.Bytes(), 0o644)
		if err != nil {
			return fmt.Errorf("writing actual.yaml file for debug purposes: %w", err)
		}
	}

	// Read expected.yaml
	expectedPath := filepath.Join(rootPath, test.name, "expected.yaml")
	expectedBytes, err := os.ReadFile(expectedPath)
	if err != nil {
		return fmt.Errorf("reading expected.yaml file: %w", err)
	}
	expectedManifest := string(expectedBytes)

	// Compile ignore patterns to regular expressions
	ignoreExpressions, err := compileIgnorePatterns(ignorePatterns)
	if err != nil {
		return fmt.Errorf("compiling ignore patterns: %w", err)
	}

	// Compare manifests
	actualManifest := manifests.String()
	isEqual, err := compareManifests(test, expectedManifest, actualManifest, ignoreExpressions)
	if err != nil {
		return fmt.Errorf("comparing manifests: %w\n\nactual manifest:\n%s\n\nexpected manifest:\n%s\n\nignore patterns:\n%v", err, actualManifest, expectedManifest, ignorePatterns)
	}
	test.SetTestComparisonResult(isEqual)

	// Update expected?
	if test.isUpdate {
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
				test.SetUpdateType("semantic")
			} else {
				test.SetUpdateType("formatting")
			}
		} else {
			test.SetUpdateType("none")
		}
	}

	// Validate
	err = validateManifest(test, release.Manifest)
	if err != nil {
		return fmt.Errorf("validating manifest: %w", err)
	}

	return nil
}

func (pb *Test) SetTestComparisonResult(isSame bool) {
	pb.isSame = isSame
}

func (pb *Test) SetUpdateType(updateType string) {
	pb.updateType = updateType
	// if pb.isUpdate {
	// 	pb.updateCounts[updateType]++
	// }
}

func (pb *Test) AddValidationError(signature, error string) {
	pb.validationErrors = append(pb.validationErrors, ValidationError{signature, error})
	pb.isValid = false
}

func (pb *Test) AddDifferentItem(source, expected, actual string) {
	pb.differentItems = append(pb.differentItems, Item{source, expected, actual})
}

func (pb *Test) AddMissingItem(source, expected string) {
	pb.missingItems = append(pb.missingItems, Item{source, expected, ""})
}

func (pb *Test) AddExtraItem(source, actual string) {
	pb.extraItems = append(pb.extraItems, Item{source, "", actual})
}

func (pb *Test) AddIgnoredLine(line string) {
	pb.ignoredLines = append(pb.ignoredLines, line)
}

const (
	separator1 = "============================================="
	separator2 = "---------------------------------------------"
	separator3 = "———————"
)

func (pb *Test) ShowValues(getValuesYaml func() (string, error)) {
	pb.getValuesYaml = getValuesYaml
}

func (pb *Test) PrintResult(longestName int) error {
	isSuccessful := pb.isSame && pb.isValid
	// if isSuccessful {
	// 	pb.successCount++
	// }

	fmt.Println(separator1)
	fmt.Printf("🧪 %s", pb.name)

	// Add padding to align the results
	padding := (longestName - len(pb.name)) + 1
	for i := 0; i < padding; i++ {
		fmt.Print(" ")
	}

	if isSuccessful {
		if pb.isUpdate {
			switch pb.updateType {
			case "none":
				fmt.Println("👍 Nothing to update in expected file")
			case "formatting":
				fmt.Println("🧹 Normalized formatting in expected file")
			default:
				fmt.Println("👍 Nothing to update in expected file")
			}
		} else {
			fmt.Println("✅  Passed")
		}
	} else {
		if pb.isUpdate {
			switch pb.updateType {
			case "semantic":
				fmt.Println("📝 Updated expected file with content changes")
			case "formatting":
				fmt.Println("🧹 Normalized formatting in expected file")
			default:
				fmt.Println("📝 Updated expected file")
			}
		} else {
			fmt.Printf("💔 Failed")
			if !pb.isValid {
				fmt.Printf("👮 Invalid")
			}
			fmt.Printf("\n")
		}
	}

	sections := 0
	if !pb.isSame {
		fmt.Println(separator2)
		if len(pb.differentItems) > 0 {
			for i, differentItem := range pb.differentItems {
				if i > 0 {
					fmt.Println(separator3)
				}
				fmt.Printf("🥸 Different %q:\n", differentItem.source)
				edits := myers.ComputeEdits(span.URIFromPath(""), differentItem.expected, differentItem.actual)
				unified := fmt.Sprintf("%s", gotextdiff.ToUnified("expected", "actual", differentItem.expected, edits))
				unified = strings.ReplaceAll(unified, "\\ No newline at end of file\n", "")
				unified = colorizeDiff(unified)
				fmt.Print(unified)
			}
			sections++
		}
		if len(pb.extraItems) > 0 {
			if sections > 0 {
				fmt.Println(separator3)
			}
			for i, extraItem := range pb.extraItems {
				if i > 0 {
					fmt.Println(separator3)
				}
				fmt.Printf("🤡 Unexpected %q:\n%s\n", extraItem.source, extraItem.actual)
			}
			sections++
		}
		if len(pb.missingItems) > 0 {
			if sections > 0 {
				fmt.Println(separator3)
			}
			for i, missingItem := range pb.missingItems {
				if i > 0 {
					fmt.Println(separator3)
				}
				fmt.Printf("🫥️ Missing %q:\n%s\n", missingItem.source, missingItem.expected)
			}
			sections++
		}
	}

	if !pb.isValid {
		if sections < 1 {
			fmt.Println(separator2)
		} else {
			fmt.Println(separator3)
		}
		for i, validationError := range pb.validationErrors {
			if i > 0 {
				fmt.Println(separator3)
			}
			fmt.Printf("🚨 Invalid %q:\n%s\n", validationError.signature, validationError.error)
		}
		sections++
	}

	// Show values for all or only failed tests
	if showAllValues || (showValues && (!pb.isSame || !pb.isValid)) {
		if sections < 1 {
			fmt.Println(separator2)
		} else {
			fmt.Println(separator3)
		}
		valuesYaml, err := pb.getValuesYaml()
		if err != nil {
			return fmt.Errorf("failed to get values yaml: %w", err)
		}
		fmt.Println("📜 Coalesced values:")
		fmt.Println(valuesYaml)
	}

	if len(pb.ignoredLines) > 0 {
		fmt.Println(separator3)
		for _, ignoredLine := range pb.ignoredLines {
			fmt.Printf("🙈 Ignored line: %q\n", ignoredLine)
		}
		pb.ignoredLines = []string{}
	}
	return nil
}

const (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
)

func colorizeDiff(diff string) string {
	var coloredDiff strings.Builder
	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "-") {
			coloredDiff.WriteString(green)
		} else if strings.HasPrefix(line, "+") {
			coloredDiff.WriteString(red)
		} else if strings.HasPrefix(line, "@") {
			coloredDiff.WriteString(yellow)
		} else {
			coloredDiff.WriteString(reset)
		}
		coloredDiff.WriteString(line)
		coloredDiff.WriteString(reset)
		coloredDiff.WriteString("\n")
	}
	return strings.TrimSpace(coloredDiff.String())
}

func (pb *Test) IsSuccessful() bool {
	return pb.isSame && pb.isValid
}

func (suite TestSuite) PrintSummary() {
	fmt.Println(separator1)
	defer fmt.Println(separator1)
	if len(suite.Tests) == 0 {
		if suite.IsUpdate {
			fmt.Println("🤷 No expected files to update")
		} else {
			fmt.Println("🤷 No tests were run")
		}
		return
	}
	if suite.IsUpdate {
		stats := suite.Updates()
		updated := stats.Formatting + stats.Formatting
		unchanged := stats.None

		if updated == 0 {
			fmt.Printf("👍 All %d expected files unchanged\n", suite.TotalLength())
			return
		}
		if unchanged == 0 {
			if stats.Semantic > 0 && stats.Formatting > 0 {
				fmt.Printf(
					"📝 Updated %d expected files (%d content changes, %d formatting normalization)\n",
					updated, stats.Semantic, stats.Formatting,
				)
				return
			}
			if stats.Semantic > 0 {
				fmt.Printf("📝 Updated %d expected files with content changes\n", stats.Semantic)
				return
			}

			fmt.Printf("🧹 Normalized formatting in %d expected files\n", stats.Formatting)
			return
		}

		if stats.Semantic > 0 && stats.Formatting > 0 {
			fmt.Printf(
				"📝 Updated %d expected files (%d content changes, %d formatting normalization), %d unchanged\n",
				updated, stats.Semantic, stats.Formatting, unchanged,
			)
			return
		}

		if stats.Semantic > 0 {
			fmt.Printf("📝 Updated %d expected files with content changes, %d unchanged\n", stats.Semantic, unchanged)
			return
		}

		fmt.Printf("🧹 Normalized formatting in %d expected files, %d unchanged\n", stats.Formatting, unchanged)
		return
	}

	// Run mode summary
	if suite.IsSuccessful() {
		fmt.Printf("🌈🦄⭐️  All %d tests passed\n", suite.TotalLength())
	} else {
		fmt.Printf("🔥👺🧨  %d tests failed out of %d\n", suite.TotalLength()-suite.TotalSuccessful(), suite.TotalLength())
	}
}

type TestSuite struct {
	IsUpdate bool
	Tests    []*Test
}

func NewTestSuite(names []string, isUpdate bool) *TestSuite {
	return &TestSuite{
		IsUpdate: isUpdate,
		Tests: func() (tests []*Test) {
			for _, name := range names {
				tests = append(tests, &Test{
					name:     name,
					isUpdate: isUpdate,
					isSame:   true,
					isValid:  true,
				})
			}
			return
		}(),
	}
}

func (suite TestSuite) TotalLength() int {
	return len(suite.Tests)
}

func (suite TestSuite) TotalSuccessful() int {
	var count int
	for _, result := range suite.Tests {
		if result.IsSuccessful() {
			count++
		}
	}
	return count
}

func (suite TestSuite) IsSuccessful() bool {
	for _, result := range suite.Tests {
		if !result.IsSuccessful() {
			return false
		}
	}
	return true
}

type UpdateStats struct {
	None       int
	Formatting int
	Semantic   int
}

func (suite TestSuite) Updates() (stats UpdateStats) {
	for _, result := range suite.Tests {
		switch result.updateType {
		case "formatting":
			stats.Formatting++
		case "semantic":
			stats.Semantic++
		default:
			stats.None++
		}
	}
	return
}

type HelmOptions struct {
	Namespace    string
	Release      string
	ChartVersion string
	AppVersion   string
}

type RunOptions struct {
	RootFS         string
	IgnorePatterns []string
	Schema         *cue.Value
	Concurrency    int
	HelmOptions
}

func (suite TestSuite) Run(opts RunOptions) error {
	longestName := func() int {
		var max int
		for _, test := range suite.Tests {
			if len(test.name) > max {
				max = len(test.name)
			}
		}
		return max
	}()

	var results []chan *Test
	for range suite.Tests {
		results = append(results, make(chan *Test, 1))
	}

	e := make(chan error, suite.TotalLength())

	concurrency := func() int {
		if opts.Concurrency <= 0 {
			return suite.TotalLength()
		}
		return opts.Concurrency
	}()

	semaphore := make(chan struct{}, concurrency)

	go func() {
		for i, test := range suite.Tests {
			semaphore <- struct{}{}
			go func() {
				defer func() { <-semaphore }()

				settings := cli.New()
				actionConfig := new(action.Configuration)

				if err := actionConfig.Init(settings.RESTClientGetter(), opts.Namespace, "memory", nil); err != nil {
					log.Fatal(err)
				}

				installAction := action.NewInstall(actionConfig)
				installAction.Namespace = opts.Namespace
				installAction.ReleaseName = opts.Release
				installAction.DryRun = true
				installAction.IncludeCRDs = true
				installAction.ClientOnly = true
				installAction.Replace = true

				chartPath, err := filepath.Abs(".")
				if err != nil {
					e <- fmt.Errorf("getting chart path: %w", err)
					return
				}

				theChart, err := loader.Load(chartPath)
				if err != nil {
					e <- fmt.Errorf("loading chart: %w", err)
					return
				}

				if chartVersion := opts.ChartVersion; chartVersion != "" {
					theChart.Metadata.Version = chartVersion
				}
				if appVersion := opts.AppVersion; appVersion != "" {
					theChart.Metadata.AppVersion = appVersion
				}

				if err := test.Run(theChart, installAction, opts.RootFS, opts.IgnorePatterns, opts.Schema); err != nil {
					e <- fmt.Errorf("running test %s: %w", test.name, err)
					return
				}

				results[i] <- test
			}()
		}
	}()

	for _, result := range results {
		select {
		case err := <-e:
			return err
		case test := <-result:
			if err := test.PrintResult(longestName); err != nil {
				return fmt.Errorf("failed to finalize test: %w", err)
			}
		}
	}

	return nil
}
