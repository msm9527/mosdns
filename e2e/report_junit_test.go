package e2e_test

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type junitTestSuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Time     string           `xml:"time,attr"`
	Suites   []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Time      string          `xml:"time,attr"`
	TestCases []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func (r *e2eReport) writeJUnit(view e2eReportView) error {
	suite := junitTestSuite{
		Name:      "mosdns.e2e",
		Tests:     len(view.Cases),
		Failures:  view.Failed,
		Time:      e2eDurationSeconds(view.Duration),
		TestCases: make([]junitTestCase, 0, len(view.Cases)),
	}
	for _, item := range view.Cases {
		tc := junitTestCase{
			Name:      item.Name,
			ClassName: "mosdns.e2e",
			Time:      e2eDurationSeconds(item.Duration),
			SystemOut: renderE2ECaseSystemOut(item),
		}
		if item.Status != "passed" {
			tc.Failure = &junitFailure{
				Message: item.Detail,
				Body:    item.Detail,
			}
		}
		suite.TestCases = append(suite.TestCases, tc)
	}
	doc := junitTestSuites{
		Tests:    suite.Tests,
		Failures: suite.Failures,
		Time:     suite.Time,
		Suites:   []junitTestSuite{suite},
	}
	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append([]byte(xml.Header), data...)
	return os.WriteFile(filepath.Join(r.outputDir, "junit.xml"), data, 0o644)
}

func e2eDurationSeconds(value string) string {
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return "0"
	}
	return fmt.Sprintf("%.3f", d.Seconds())
}

func renderE2ECaseSystemOut(item e2eCaseReport) string {
	lines := []string{
		"detail: " + item.Detail,
		"artifact: " + item.Artifact,
	}
	if len(item.Checks) > 0 {
		lines = append(lines, "checks:")
		for _, check := range item.Checks {
			lines = append(lines, "  - "+check.Name+": "+check.Detail)
		}
	}
	if len(item.Metrics) > 0 {
		lines = append(lines, "metrics:")
		for _, metric := range item.Metrics {
			lines = append(lines, "  - "+metric.Name+" = "+metric.Value+" ("+metric.Detail+")")
		}
	}
	return strings.Join(lines, "\n")
}
