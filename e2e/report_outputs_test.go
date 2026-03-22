package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func prepareE2EReportView(outputDir string, view e2eReportView) (e2eReportView, error) {
	view.Environment = buildE2EEnvironment(outputDir)
	cases, caseArtifacts, err := writeE2ECaseArtifacts(outputDir, view.Cases)
	if err != nil {
		return e2eReportView{}, err
	}
	view.Cases = cases
	view.Artifacts = append(buildE2EStaticArtifacts(), caseArtifacts...)
	return enrichE2EReportView(view), nil
}

func buildE2EEnvironment(outputDir string) e2eEnvironmentInfo {
	ci := firstNonEmptyEnv("CI")
	if ci == "" {
		ci = "false"
	}
	return e2eEnvironmentInfo{
		FixtureMode: "full-config-fixture",
		GoVersion:   runtime.Version(),
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		CI:          ci,
		Commit:      shortE2ECommit(firstNonEmptyEnv("GITHUB_SHA", "CI_COMMIT_SHA")),
		Ref:         firstNonEmptyEnv("GITHUB_REF_NAME", "CI_COMMIT_REF_NAME"),
		ReportDir:   outputDir,
	}
}

func buildE2EStaticArtifacts() []e2eArtifactReport {
	return []e2eArtifactReport{
		{Name: "HTML Report", Kind: "html", Path: "index.html"},
		{Name: "HTML Stylesheet", Kind: "css", Path: "report.css"},
		{Name: "HTML Renderer Script", Kind: "javascript", Path: "report_renderer.js"},
		{Name: "HTML Script", Kind: "javascript", Path: "report.js"},
		{Name: "JSON Report", Kind: "json", Path: "report.json"},
		{Name: "JUnit XML", Kind: "junit", Path: "junit.xml"},
	}
}

func writeE2ECaseArtifacts(outputDir string, cases []e2eCaseReport) ([]e2eCaseReport, []e2eArtifactReport, error) {
	artifactDir := filepath.Join(outputDir, "artifacts", "cases")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return nil, nil, err
	}
	outCases := append([]e2eCaseReport(nil), cases...)
	artifacts := make([]e2eArtifactReport, 0, len(outCases))
	for i := range outCases {
		name := slugifyE2EName(outCases[i].Name)
		relPath := filepath.Join("artifacts", "cases", name+".json")
		if err := writeE2ECaseArtifact(filepath.Join(outputDir, relPath), outCases[i]); err != nil {
			return nil, nil, err
		}
		outCases[i].Artifact = relPath
		artifacts = append(artifacts, e2eArtifactReport{
			Name: outCases[i].Name + " Artifact",
			Kind: "case-json",
			Path: relPath,
		})
	}
	return outCases, artifacts, nil
}

func writeE2ECaseArtifact(path string, item e2eCaseReport) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func slugifyE2EName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if lastDash {
			continue
		}
		b.WriteByte('-')
		lastDash = true
	}
	value := strings.Trim(b.String(), "-")
	if value == "" {
		return "case"
	}
	return value
}

func shortE2ECommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}
