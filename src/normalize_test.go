package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeManifest(t *testing.T) {
	original := `---
# Source: valid/templates/test.yaml
test: value
---
# Source: valid/templates/service.yaml
apiVersion: v1
kind: Service
metadata:
    # Comment
    name: my-release
    namespace: "my-namespace"
spec:
        selector:
            app: 'my-release'
            version: v1
---
# Source: valid/templates/service.yaml
apiVersion: v1
kind: Service
metadata: { "name": "my-release-2", "namespace": "my-namespace" }
spec:
  selector:
    app: my-release
    version: v1

`
	expected := `---
# Source: valid/templates/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: my-release
  namespace: my-namespace
spec:
  selector:
    app: my-release
    version: v1
---
apiVersion: v1
kind: Service
metadata:
  name: my-release-2
  namespace: my-namespace
spec:
  selector:
    app: my-release
    version: v1
---
# Source: valid/templates/test.yaml
test: value`

	actual, err := normalizeManifest(original)
	if err != nil {
		t.Fatalf("error normalizing manifest: %v", err)
	}
	assert.Equal(t, expected, actual, "normalized manifest should match expected output")
}
