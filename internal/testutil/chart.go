package testutil

import (
	"fmt"
	"os"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	helmchart "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	helmregistry "helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
)

// TestPullAndTemplateChart pulls a chart from an OCI registry and optionally templates it.
// It returns the loaded chart and optionally the templated release if templating was requested.
func TestPullAndTemplateChart(ociRef string, expectedChartName string, skipTemplating bool) (*helmchart.Chart, *release.Release, error) {
	// Setup helm environment
	settings := cli.New()

	// Initialize registry client
	helmReg, err := helmregistry.NewClient(
		helmregistry.ClientOptDebug(false),
		helmregistry.ClientOptEnableCache(true),
		helmregistry.ClientOptCredentialsFile(""),
		helmregistry.ClientOptPlainHTTP(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create registry client: %w", err)
	}

	// Initialize action configuration
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), settings.Namespace(), os.Getenv("HELM_DRIVER"), func(format string, v ...interface{}) {
		fmt.Printf(format, v...)
	}); err != nil {
		return nil, nil, fmt.Errorf("failed to initialize action configuration: %w", err)
	}
	actionConfig.RegistryClient = helmReg

	// Set up the pull action
	pull := action.NewPullWithOpts(action.WithConfig(actionConfig))
	pull.Settings = settings
	tmpDir, err := os.MkdirTemp("", "helm-test-")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	pull.DestDir = tmpDir
	pull.PlainHTTP = true

	// Pull the chart using OCI reference
	fmt.Printf("Pulling chart from %s to %s\n", ociRef, tmpDir)
	if _, err := pull.Run(ociRef); err != nil {
		return nil, nil, fmt.Errorf("failed to pull chart: %w", err)
	}

	// Find the chart file
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read directory %s: %w", tmpDir, err)
	}

	var chartPath string
	for _, entry := range entries {
		fmt.Printf("Found file in temp dir: %s\n", entry.Name())
		if strings.HasSuffix(entry.Name(), ".tgz") {
			chartPath = fmt.Sprintf("%s/%s", tmpDir, entry.Name())
			break
		}
	}

	if chartPath == "" {
		return nil, nil, fmt.Errorf("no chart file found in directory %s", tmpDir)
	}

	fmt.Printf("Loading chart from: %s\n", chartPath)

	// Load the chart
	helmChart, err := loader.Load(chartPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load chart: %w", err)
	}

	// Debug chart info
	fmt.Printf("Chart loaded: %s version %s\n", helmChart.Name(), helmChart.Metadata.Version)

	// Verify the chart name
	if expectedChartName != "" && helmChart.Metadata.Name != expectedChartName {
		return nil, nil, fmt.Errorf("expected chart name %s but got %s", expectedChartName, helmChart.Metadata.Name)
	}

	// Skip templating for library charts or if explicitly requested
	if skipTemplating || helmChart.Metadata.Type == "library" {
		return helmChart, nil, nil
	}

	// Create install action for templating
	install := action.NewInstall(actionConfig)
	install.DryRun = true
	install.ReleaseName = "test-release"
	install.Replace = true
	install.ClientOnly = true

	// Run template
	rel, err := install.Run(helmChart, nil)
	if err != nil {
		return helmChart, nil, fmt.Errorf("failed to template chart: %w", err)
	}

	return helmChart, rel, nil
}
