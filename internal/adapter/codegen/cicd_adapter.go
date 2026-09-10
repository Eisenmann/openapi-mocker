package codegen

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// CICDPlatform identifies a CI/CD system.
type CICDPlatform string

const (
	PlatformGitHubActions CICDPlatform = "github"
	PlatformGitLabCI      CICDPlatform = "gitlab"
	PlatformJenkins       CICDPlatform = "jenkins"
	PlatformCircleCI      CICDPlatform = "circleci"
)

// openAPIGeneratorURL is the pinned openapi-generator-cli download URL.
// Hoisted into a const to keep template lines under the lll limit and make
// the pinned version configurable in one place.
const openAPIGeneratorURL = "https://repo1.maven.org/maven2/org/openapitools/" +
	"openapi-generator-cli/7.0.0/openapi-generator-cli-7.0.0.jar"

// CICDAdapter generates CI/CD pipeline configuration that runs code
// generation as a pipeline step. It implements ports.CodeGeneratorPort.
type CICDAdapter struct {
	platform  CICDPlatform
	templates map[CICDPlatform]*template.Template
}

// NewCICDAdapter creates a CI/CD adapter for the given platform.
func NewCICDAdapter(platform CICDPlatform) *CICDAdapter {
	return &CICDAdapter{
		platform:  platform,
		templates: loadCICDTemplates(),
	}
}

// Supports reports whether this adapter supports the given language.
// CI/CD supports all languages via openapi-generator.
func (a *CICDAdapter) Supports(_ ports.Language) bool {
	return true
}

// Strategy returns the generation strategy used by this adapter.
func (a *CICDAdapter) Strategy() ports.GenerationStrategy {
	return ports.StrategyCICD
}

// Generate produces a CI/CD pipeline configuration file.
func (a *CICDAdapter) Generate(
	_ context.Context,
	req *ports.GenerationRequest,
) (*ports.GenerationResult, error) {
	cicdFile, err := a.generateCICDConfig(req)
	if err != nil {
		return nil, err
	}

	cicdConfig := a.buildCICDConfig(req)

	return &ports.GenerationResult{
		Files:      []ports.GeneratedFile{*cicdFile},
		Language:   req.Language,
		Strategy:   ports.StrategyCICD,
		CICDConfig: cicdConfig,
		Warnings: []string{
			"Code generation configured as CI/CD step.",
			"Run the pipeline to generate code for " + string(req.Language),
		},
	}, nil
}

// generateCICDConfig renders the pipeline template for the platform.
func (a *CICDAdapter) generateCICDConfig(
	req *ports.GenerationRequest,
) (*ports.GeneratedFile, error) {
	tmpl, ok := a.templates[a.platform]
	if !ok {
		return nil, fmt.Errorf("%w: %s", usecase.ErrUnsupportedPlatform, a.platform)
	}

	data := CICDTemplateData{
		Language:     req.Language,
		PackageName:  req.PackageName,
		OutputPath:   req.OutputPath,
		SchemaPath:   "./api/openapi.yaml",
		ProjectName:  req.Metadata.ProjectName,
		GeneratorMap: defaultGeneratorMap(),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}

	filePath, fileName := a.getCICDFilePath()

	path := fileName
	if filePath != "" {
		path = filePath + "/" + fileName
	}

	return &ports.GeneratedFile{
		Path:    path,
		Content: buf.Bytes(),
		IsNew:   true,
	}, nil
}

// getCICDFilePath returns the directory and filename for the platform.
func (a *CICDAdapter) getCICDFilePath() (dir, fileName string) {
	switch a.platform {
	case PlatformGitHubActions:
		return ".github/workflows", "codegen.yml"
	case PlatformGitLabCI:
		return "", ".gitlab-ci.yml"
	case PlatformJenkins:
		return "", "Jenkinsfile"
	case PlatformCircleCI:
		return ".circleci", "config.yml"
	default:
		return "", ""
	}
}

// buildCICDConfig constructs the structured CI/CD configuration.
func (a *CICDAdapter) buildCICDConfig(req *ports.GenerationRequest) *ports.CICDConfig {
	generatorName := defaultGeneratorMap()[req.Language]

	return &ports.CICDConfig{
		Platform: string(a.platform),
		Commands: []string{
			fmt.Sprintf(
				"java -jar openapi-generator-cli.jar generate -g %s -i ./api/openapi.yaml -o %s",
				generatorName,
				req.OutputPath,
			),
		},
		StepConfig: map[string]string{
			"generator": generatorName,
			"language":  string(req.Language),
		},
	}
}

// CICDTemplateData is the data passed to CI/CD templates.
type CICDTemplateData struct {
	Language     ports.Language
	PackageName  string
	OutputPath   string
	SchemaPath   string
	ProjectName  string
	GeneratorMap map[ports.Language]string
}

// loadCICDTemplates builds the template map for supported platforms.
func loadCICDTemplates() map[CICDPlatform]*template.Template {
	templates := make(map[CICDPlatform]*template.Template)

	templates[PlatformGitHubActions] = template.Must(
		template.New("github").Parse(githubActionsTemplate),
	)

	templates[PlatformGitLabCI] = template.Must(
		template.New("gitlab").Parse(gitlabCITemplate),
	)

	templates[PlatformJenkins] = template.Must(
		template.New("jenkins").Parse(jenkinsTemplate),
	)

	templates[PlatformCircleCI] = template.Must(
		template.New("circleci").Parse(circleCITemplate),
	)

	return templates
}

const githubActionsTemplate = `name: Code Generation - {{.Language}}

on:
  push:
    paths:
      - '{{.SchemaPath}}'
  workflow_dispatch:

jobs:
  generate-{{.Language}}-client:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Setup Java
        uses: actions/setup-java@v3
        with:
          java-version: '17'

      - name: Download OpenAPI Generator
        run: |
          wget ` + openAPIGeneratorURL + ` \
            -O openapi-generator-cli.jar

      - name: Generate {{.Language}} Client
        run: |
          java -jar openapi-generator-cli.jar generate \
            -g {{index .GeneratorMap .Language}} \
            -i {{.SchemaPath}} \
            -o {{.OutputPath}} \
            --package-name={{.PackageName}}

      - name: Create Pull Request
        uses: peter-evans/create-pull-request@v5
        with:
          title: 'chore: update {{.Language}} generated client'
          branch: codegen/{{.Language}}-update
`

const gitlabCITemplate = `stages:
  - codegen

generate-{{.Language}}:
  stage: codegen
  image: openjdk:17
  script:
    - wget ` + openAPIGeneratorURL + ` -O openapi-generator-cli.jar
    - java -jar openapi-generator-cli.jar generate
        -g {{index .GeneratorMap .Language}}
        -i {{.SchemaPath}}
        -o {{.OutputPath}}
  artifacts:
    paths:
      - {{.OutputPath}}
  only:
    changes:
      - {{.SchemaPath}}
`

const jenkinsTemplate = `pipeline {
    agent any

    stages {
        stage('Generate {{.Language}} Client') {
            steps {
                sh '''
                    wget ` + openAPIGeneratorURL + ` -O openapi-generator-cli.jar
                    java -jar openapi-generator-cli.jar generate \\
                        -g {{index .GeneratorMap .Language}} \\
                        -i {{.SchemaPath}} \\
                        -o {{.OutputPath}} \\
                        --package-name={{.PackageName}}
                '''
            }
        }
    }
}
`

const circleCITemplate = `version: 2.1

jobs:
  generate-{{.Language}}:
    docker:
      - image: openjdk:17
    steps:
      - checkout
      - run:
          name: Download OpenAPI Generator
          command: |
            wget ` + openAPIGeneratorURL + ` -O openapi-generator-cli.jar
      - run:
          name: Generate {{.Language}} Client
          command: |
            java -jar openapi-generator-cli.jar generate \\
              -g {{index .GeneratorMap .Language}} \\
              -i {{.SchemaPath}} \\
              -o {{.OutputPath}} \\
              --package-name={{.PackageName}}
      - store_artifacts:
          path: {{.OutputPath}}

workflows:
  codegen:
    jobs:
      - generate-{{.Language}}:
          filters:
            paths:
              - {{.SchemaPath}}
`
