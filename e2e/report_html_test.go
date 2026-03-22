package e2e_test

import (
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
)

const (
	e2eReportTemplatePath = "report.html.tmpl"
	e2eReportCSSPath      = "report.css"
	e2eReportHelperJSPath = "report_renderer.js"
	e2eReportJSPath       = "report.js"
)

type e2eHTMLReportPage struct {
	Title      string
	ReportJSON template.JS
}

func writeE2EHTMLReport(outputDir string, view e2eReportView) error {
	if err := writeE2EHTMLAssets(outputDir); err != nil {
		return err
	}
	page, err := buildE2EHTMLReportPage(view)
	if err != nil {
		return err
	}
	tmpl, err := template.ParseFiles(filepath.Join("testdata", e2eReportTemplatePath))
	if err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(outputDir, "index.html"))
	if err != nil {
		return err
	}
	defer file.Close()
	return tmpl.Execute(file, page)
}

func buildE2EHTMLReportPage(view e2eReportView) (e2eHTMLReportPage, error) {
	data, err := json.Marshal(view)
	if err != nil {
		return e2eHTMLReportPage{}, err
	}
	return e2eHTMLReportPage{
		Title:      "mosdns E2E 中文报告",
		ReportJSON: template.JS(data),
	}, nil
}

func writeE2EHTMLAssets(outputDir string) error {
	assets := []string{e2eReportCSSPath, e2eReportHelperJSPath, e2eReportJSPath}
	for _, name := range assets {
		if err := copyE2EHTMLAsset(outputDir, name); err != nil {
			return err
		}
	}
	return nil
}

func copyE2EHTMLAsset(outputDir, name string) error {
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, name), data, 0o644)
}
