# testchart

Testchart is a CLI tool for creating and running helm chart unit tests.

It uses `helm` under the hood (but it has no external dependencies) to render chart in current directory once for each test found in the chart's `tests` sub-directory, using the `values.yaml` file in the test folder as inputs, and doing a byte-for-byte comparison against the rendered results against the `expected.yaml` file also in the test directory.  The name of each test directory is used as the test name.

For example, consider this directory structure:
```
tests
├── deployment-with-secrets
│   ├── values.yaml
│   └── expected.yaml
├── deployment-without-secrets
│   ├── values.yaml
│   └── expected.yaml
├── job-with-sidecar
│   ├── values.yaml
│   └── expected.yaml
└── job-without-sidecar
    ├── values.yaml
    └── expected.yaml
```

It defines the following test cases:
- `deployment-with-secrets`
- `deployment-without-secrets`
- `job-with-sidecar`
- `job-without-sidecar`

For each test, the given `values.yaml` file will be injected into the chart and the resulting yaml compared against the given `expected.yaml` file.

# Installation

## Using `homebrew`

To install:
```bash
$ brew tap silphid/tap
$ brew install testchart
```

To upgrade:
```bash
$ brew update
$ brew upgrade testchart
```

## Manual installation

Download and install manually from latest release page on GitHub: https://github.com/silphid/testchart/releases/latest

# Usage

```
$ testchart --help

Tests helm charts

Usage:
  testchart [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  run         Run unit tests
  update      Update expected files
  version     Display testchart build version

Flags:
      --app-version string     App version of chart to override for rendering chart
      --chart-version string   Version of chart to override for rendering chart
  -h, --help                   help for testchart
  -i, --ignore strings         Regex specifying lines to ignore (can be specified multiple times)
  -n, --namespace string       Name of namespace to use for rendering chart (default "my-namespace")
  -p, --path string            Path to tests directory (default "tests")
  -r, --release string         Name of release to use for rendering chart (default "my-release")
  -s, --save-actual            Saves an actual.yaml file in each test dir for troubleshooting
  -V, --show-all-values        Shows coalesced values for all tests
  -v, --show-values            Shows coalesced values for failed tests

Use "testchart [command] --help" for more information about a command.
```

## Comparison Method

When comparing the rendered results against the expected results, `testchart` tests literal byte-for-byte equality;
it does not use YAML equality nor does it perform any normalization.

## Run all tests

To run all tests under chart's `tests` sub-directory:

```bash
$ testchart run
```

## Run specific tests

To run specific tests under chart's `tests` sub-directory:

```bash
$ testchart run test1 test2 ...
```

## Update all expected files

Watch out, as this will overwrite all tests expected files to match rendered manifests.

```bash
$ testchart update
```

## Generate expected file for specific test

To generate the `expected.yaml` for the first time for a new test named `test1`:

```bash
$ testchart update test1
```
