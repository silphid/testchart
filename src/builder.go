package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

type Builder interface {
	StartAllTests(names []string)

	StartTest(name string)

	SetTestComparisonResult(isSame bool)
	SetUpdateType(updateType string)
	AddValidationError(signature, error string)

	AddDifferentItem(source, expected, actual string)
	AddMissingItem(source, expected string)
	AddExtraItem(source, actual string)
	AddIgnoredLine(line string)

	ShowValues(getValuesYaml func() (string, error))

	EndTest() error

	EndAllTests()
	IsSuccessful() bool
}

type Item struct {
	source, expected, actual string
}

type ValidationError struct {
	signature, error string
}

func NewPrintBuilder(isUpdate bool) *PrintBuilder {
	return &PrintBuilder{
		isUpdate:     isUpdate,
		updateCounts: make(map[string]int),
	}
}

type PrintBuilder struct {
	name                                     string
	isUpdate                                 bool
	updateType                               string
	isSame, isValid                          bool
	differentItems, missingItems, extraItems []Item
	validationErrors                         []ValidationError
	getValuesYaml                            func() (string, error)
	testCount, successCount                  int
	updateCounts                             map[string]int // Track update types: "none", "formatting", "semantic"
	longestName                              int
	ignoredLines                             []string
}

func (pb *PrintBuilder) StartAllTests(names []string) {
	pb.testCount = 0
	pb.successCount = 0
	pb.updateCounts = make(map[string]int)
	pb.updateCounts["none"] = 0
	pb.updateCounts["formatting"] = 0
	pb.updateCounts["semantic"] = 0

	// Calculate longest name
	for _, name := range names {
		if len(name) > pb.longestName {
			pb.longestName = len(name)
		}
	}
}

func (pb *PrintBuilder) StartTest(name string) {
	pb.name = name
	pb.isValid = true
	pb.isSame = true
	pb.updateType = ""
	pb.differentItems = nil
	pb.missingItems = nil
	pb.extraItems = nil
	pb.validationErrors = nil
	pb.testCount++
}

func (pb *PrintBuilder) SetTestComparisonResult(isSame bool) {
	pb.isSame = isSame
}

func (pb *PrintBuilder) SetUpdateType(updateType string) {
	pb.updateType = updateType
	if pb.isUpdate {
		pb.updateCounts[updateType]++
	}
}

func (pb *PrintBuilder) AddValidationError(signature, error string) {
	pb.validationErrors = append(pb.validationErrors, ValidationError{signature, error})
	pb.isValid = false
}

func (pb *PrintBuilder) AddDifferentItem(source, expected, actual string) {
	pb.differentItems = append(pb.differentItems, Item{source, expected, actual})
}

func (pb *PrintBuilder) AddMissingItem(source, expected string) {
	pb.missingItems = append(pb.missingItems, Item{source, expected, ""})
}

func (pb *PrintBuilder) AddExtraItem(source, actual string) {
	pb.extraItems = append(pb.extraItems, Item{source, "", actual})
}

func (pb *PrintBuilder) AddIgnoredLine(line string) {
	pb.ignoredLines = append(pb.ignoredLines, line)
}

const (
	separator1 = "============================================="
	separator2 = "---------------------------------------------"
	separator3 = "———————"
)

func (pb *PrintBuilder) ShowValues(getValuesYaml func() (string, error)) {
	pb.getValuesYaml = getValuesYaml
}

func (pb *PrintBuilder) EndTest() error {
	isSuccessful := pb.isSame && pb.isValid
	if isSuccessful {
		pb.successCount++
	}

	fmt.Println(separator1)
	fmt.Printf("🧪 %s", pb.name)

	// Add padding to align the results
	padding := (pb.longestName - len(pb.name)) + 1
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

func (pb *PrintBuilder) EndAllTests() {
	fmt.Println(separator1)
	if pb.testCount == 0 {
		if pb.isUpdate {
			fmt.Println("🤷 No expected files to update")
		} else {
			fmt.Println("🤷 No tests were run")
		}
	} else if pb.isUpdate {
		// Update mode summary
		updated := pb.updateCounts["semantic"] + pb.updateCounts["formatting"]
		unchanged := pb.updateCounts["none"]

		if updated == 0 {
			fmt.Printf("👍 All %d expected files unchanged\n", pb.testCount)
		} else if unchanged == 0 {
			if pb.updateCounts["semantic"] > 0 && pb.updateCounts["formatting"] > 0 {
				fmt.Printf("📝 Updated %d expected files (%d content changes, %d formatting normalization)\n",
					updated, pb.updateCounts["semantic"], pb.updateCounts["formatting"])
			} else if pb.updateCounts["semantic"] > 0 {
				fmt.Printf("📝 Updated %d expected files with content changes\n", pb.updateCounts["semantic"])
			} else {
				fmt.Printf("🧹 Normalized formatting in %d expected files\n", pb.updateCounts["formatting"])
			}
		} else {
			if pb.updateCounts["semantic"] > 0 && pb.updateCounts["formatting"] > 0 {
				fmt.Printf("📝 Updated %d expected files (%d content changes, %d formatting normalization), %d unchanged\n",
					updated, pb.updateCounts["semantic"], pb.updateCounts["formatting"], unchanged)
			} else if pb.updateCounts["semantic"] > 0 {
				fmt.Printf("📝 Updated %d expected files with content changes, %d unchanged\n", pb.updateCounts["semantic"], unchanged)
			} else {
				fmt.Printf("🧹 Normalized formatting in %d expected files, %d unchanged\n", pb.updateCounts["formatting"], unchanged)
			}
		}
	} else {
		// Run mode summary
		if pb.IsSuccessful() {
			fmt.Printf("🌈🦄⭐️  All %d tests passed\n", pb.testCount)
		} else {
			fmt.Printf("🔥👺🧨  %d tests failed out of %d\n", pb.testCount-pb.successCount, pb.testCount)
		}
	}
	fmt.Println(separator1)
}

func (pb *PrintBuilder) IsSuccessful() bool {
	return pb.successCount == pb.testCount
}

type syncBuilder struct {
	builder Builder
	mu      sync.Mutex
}

func newSyncBuilder(builder Builder) *syncBuilder {
	return &syncBuilder{builder: builder}
}

func (sb *syncBuilder) StartAllTests(names []string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.StartAllTests(names)
}

func (sb *syncBuilder) StartTest(name string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.StartTest(name)
}

func (sb *syncBuilder) SetTestComparisonResult(isSame bool) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.SetTestComparisonResult(isSame)
}

func (sb *syncBuilder) SetUpdateType(updateType string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.SetUpdateType(updateType)
}

func (sb *syncBuilder) AddValidationError(signature, error string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.AddValidationError(signature, error)
}

func (sb *syncBuilder) AddDifferentItem(source, expected, actual string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.AddDifferentItem(source, expected, actual)
}

func (sb *syncBuilder) AddMissingItem(source, expected string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.AddMissingItem(source, expected)
}

func (sb *syncBuilder) AddExtraItem(source, actual string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.AddExtraItem(source, actual)
}

func (sb *syncBuilder) AddIgnoredLine(line string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.AddIgnoredLine(line)
}

func (sb *syncBuilder) ShowValues(getValuesYaml func() (string, error)) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.ShowValues(getValuesYaml)
}

func (sb *syncBuilder) EndTest() error {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.builder.EndTest()
}

func (sb *syncBuilder) EndAllTests() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.builder.EndAllTests()
}

func (sb *syncBuilder) IsSuccessful() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.builder.IsSuccessful()
}
