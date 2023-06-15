package main

import (
	"fmt"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"strings"
)

type Builder interface {
	StartAllTests(names []string)

	StartTest(name string)

	SetTestComparisonResult(isSame bool)
	AddValidationError(signature, error string)

	AddDifferentItem(source, expected, actual string)
	AddMissingItem(source, expected string)
	AddExtraItem(source, actual string)

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
	return &PrintBuilder{isUpdate: isUpdate}
}

type PrintBuilder struct {
	name                                     string
	isUpdate                                 bool
	isSame, isValid                          bool
	differentItems, missingItems, extraItems []Item
	validationErrors                         []ValidationError
	getValuesYaml                            func() (string, error)
	testCount, successCount                  int
	longestName                              int
}

func (pb *PrintBuilder) StartAllTests(names []string) {
	pb.testCount = 0
	pb.successCount = 0

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
	pb.differentItems = nil
	pb.missingItems = nil
	pb.extraItems = nil
	pb.validationErrors = nil
	pb.testCount++
}

func (pb *PrintBuilder) SetTestComparisonResult(isSame bool) {
	pb.isSame = isSame
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
			fmt.Println("👍 Nothing to update in expected file")
		} else {
			fmt.Println("✅  Passed")
		}
	} else {
		if pb.isUpdate {
			fmt.Println("📝 Updated expected file")
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
		fmt.Println("🤷 No tests were run")
	} else if pb.IsSuccessful() {
		fmt.Printf("🌈🦄☀️  All %d tests passed\n", pb.testCount)
	} else {
		fmt.Printf("🔥👺🧨  %d tests failed out of %d\n", pb.testCount-pb.successCount, pb.testCount)
	}
	fmt.Println(separator1)
}

func (pb *PrintBuilder) IsSuccessful() bool {
	return pb.successCount == pb.testCount
}
