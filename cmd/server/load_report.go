package main

import (
	"encoding/json"
	"fmt"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/engine"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/report"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// renderReportFiles returns the report files by name for the formats asked.
func renderReportFiles(formats []domain.OutputFormat, out *usecase.LoadOutcome) (map[string][]byte, error) {
	files := make(map[string][]byte, len(formats))

	var rep *report.Report

	for _, f := range formats {
		switch f {
		case domain.OutputJSON:
			files["report.json"] = out.Artifacts[engine.ArtifactSummary]
		case domain.OutputCSV:
			files["timeseries.csv"] = out.Artifacts[engine.ArtifactSeries]
		case domain.OutputHTML, domain.OutputJUnit:
			if rep == nil {
				rep = new(report.Report)

				err := json.Unmarshal(out.Artifacts[engine.ArtifactSummary], rep)
				if err != nil {
					return nil, fmt.Errorf("read report: %w", err)
				}
			}

			if err := renderFromReport(files, f, rep); err != nil {
				return nil, err
			}
		}
	}

	return files, nil
}

func renderFromReport(files map[string][]byte, f domain.OutputFormat, rep *report.Report) error {
	var (
		data []byte
		err  error
	)

	name := "report.html"

	if f == domain.OutputJUnit {
		name = "junit.xml"
		data, err = report.JUnit(rep)
	} else {
		data, err = report.HTML(rep)
	}

	if err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}

	files[name] = data

	return nil
}
