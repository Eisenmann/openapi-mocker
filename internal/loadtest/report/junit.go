package report

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Name    string       `xml:"name,attr"`
	Tests   int          `xml:"tests,attr"`
	Failed  int          `xml:"failures,attr"`
	Time    string       `xml:"time,attr"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name   string      `xml:"name,attr"`
	Tests  int         `xml:"tests,attr"`
	Failed int         `xml:"failures,attr"`
	Time   string      `xml:"time,attr"`
	Cases  []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

// JUnit renders one test case per threshold, so a CI system shows which
// threshold of the run failed. A run that was stopped before its end adds a
// failing case for that.
func JUnit(r *Report) ([]byte, error) {
	suite := new(junitSuite)
	suite.Name = r.Run.Name
	suite.Time = seconds(r.Run.DurationSeconds)

	for i := range r.Thresholds {
		suite.Cases = append(suite.Cases, thresholdCase(r, &r.Thresholds[i]))
	}

	if c := runCase(r); c != nil {
		suite.Cases = append(suite.Cases, *c)
	}

	for i := range suite.Cases {
		if suite.Cases[i].Failure != nil {
			suite.Failed++
		}
	}

	suite.Tests = len(suite.Cases)

	all := new(junitSuites)
	all.Name = r.Run.Name
	all.Tests = suite.Tests
	all.Failed = suite.Failed
	all.Time = suite.Time
	all.Suites = []junitSuite{*suite}

	out, err := xml.MarshalIndent(all, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode junit: %w", err)
	}

	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// thresholdName is how a threshold reads: "http.latency.p95{checkout} < 500".
func thresholdName(t *Threshold) string {
	name := t.Metric
	if t.Scope != "" {
		name += "{" + t.Scope + "}"
	}

	return name + " " + t.Op + " " + strconv.FormatFloat(t.Value, 'f', -1, 64)
}

func thresholdCase(r *Report, t *Threshold) junitCase {
	c := new(junitCase)
	c.Name = thresholdName(t)
	c.ClassName = r.Run.Name
	c.Time = "0"

	if t.Passed {
		return *c
	}

	msg := "no data was measured"
	if t.Observed != nil {
		msg = "observed " + strconv.FormatFloat(*t.Observed, 'f', -1, 64)
	}

	c.Failure = &junitFailure{Message: msg, Type: "threshold", Text: thresholdName(t) + ": " + msg}

	return *c
}

// runCase reports a run that did not complete normally.
func runCase(r *Report) *junitCase {
	var detail string

	switch {
	case r.Run.Error != "":
		detail = "run failed: " + r.Run.Error
	case r.Run.State == "aborted":
		detail = "run aborted: " + r.Run.AbortReason
	default:
		return nil
	}

	detail = strings.TrimSpace(detail)

	return &junitCase{
		Name: "run completed", ClassName: r.Run.Name, Time: "0",
		Failure: &junitFailure{Message: detail, Type: "run", Text: detail},
	}
}

func seconds(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
