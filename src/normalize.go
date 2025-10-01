package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func normalizeManifest(manifest string) (string, error) {
	// Split manifest into individual documents, normalize each, then rejoin
	documents := splitManifest(manifest)
	var normalizedParts []string

	// Get sources in a consistent order
	var sources []string
	for source := range documents {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	for _, source := range sources {
		content := documents[source]
		normalizedContent, err := normalizeYAML(content)
		if err != nil {
			return "", fmt.Errorf("normalizing YAML: %w", err)
		}

		// Reconstruct the document with source header
		normalizedParts = append(normalizedParts, "---\n# Source: "+source+"\n"+normalizedContent)
	}

	return strings.Join(normalizedParts, "\n"), nil
}

func normalizeYAML(content string) (string, error) {
	var (
		builder strings.Builder
		encoder = yaml.NewEncoder(&builder)
		decoder = yaml.NewDecoder(strings.NewReader(content))
	)
	encoder.SetIndent(2)
	for {
		var elem any
		if err := decoder.Decode(&elem); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("failed to decode values: %w", err)
		}
		if err := encoder.Encode(elem); err != nil {
			return "", fmt.Errorf("failed to encode values: %w", err)
		}
	}
	content = builder.String()
	return strings.TrimSpace(content), nil
}
