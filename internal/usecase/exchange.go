package usecase

import (
	"fmt"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Kinds of importable files (ExchangeCodec.DetectImport).
const (
	KindBundle   = "bundle"
	KindPostman  = "postman"
	KindInsomnia = "insomnia"
	KindHAR      = "har"
	KindOpenAPI  = "openapi"
)

// Formats of ExportCollection.
const (
	CollectionPostman  = "postman"
	CollectionInsomnia = "insomnia"
)

// Limits of imported files.
const (
	MaxImportBytes    = 32 << 20
	maxImportEndpoint = 2000
	maxBundleMocks    = 10000
	maxBundleVersions = 500
	maxImportedBody   = 1 << 20
)

// ImportedAPI is what an importer reads out of a Postman collection, an
// Insomnia export or a HAR file: the endpoints and the example exchanges.
type ImportedAPI struct {
	Name      string
	Endpoints []ImportedEndpoint
	Warnings  []string
}

// ImportedEndpoint is one method + templated path ("/users/{id}").
type ImportedEndpoint struct {
	Method  string
	Path    string
	Summary string
	// PathTypes gives the JSON type ("integer" or "string") of path
	// parameters whose sample value is known.
	PathTypes map[string]string
	Query     []string
	// Request is an example request body, if any.
	Request   *ImportedBody
	Responses []ImportedResponse
}

// ImportedBody is a request body example.
type ImportedBody struct {
	ContentType string
	Text        string
}

// ImportedResponse is an example response.
type ImportedResponse struct {
	Status      int
	ContentType string
	Headers     map[string][]string
	Body        string
}

// ExportData is a project as the collection exporters see it.
type ExportData struct {
	Name string
	// BaseURL is where the project's mock server answers,
	// e.g. http://localhost:8080/mock/<projectId>.
	BaseURL   string
	Endpoints []Endpoint
	Mocks     []*domain.MockRule
}

// ExchangeCodec reads and writes the third-party file formats. It is
// implemented by an adapter, so the usecase layer knows none of them.
type ExchangeCodec interface {
	// DetectImport says what kind of file data is (one of the Kind constants).
	DetectImport(data []byte) (string, error)
	// ParseImport reads a Postman, Insomnia or HAR file.
	ParseImport(kind string, data []byte) (*ImportedAPI, error)
	// ExportCollection writes the project as a Postman or Insomnia collection.
	ExportCollection(format string, data *ExportData) ([]byte, error)
}

// Bundle is the single-file form of a project: its settings, contract
// versions and mocks. It carries no request logs, state data, drift reports,
// LLM providers or upstream URL.
type Bundle struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	ExportedAt time.Time         `json:"exportedAt"`
	Project    BundleProject     `json:"project"`
	Contracts  []BundleContract  `json:"contracts"`
	Mocks      []domain.MockRule `json:"mocks"`
}

// BundleFormat and BundleVersion identify the bundle file.
const (
	BundleFormat  = "openapi-mocker.bundle"
	BundleVersion = 1
)

// BundleProject holds the project settings that travel with a bundle.
type BundleProject struct {
	Name           string                `json:"name"`
	Description    string                `json:"description"`
	ValidationMode domain.ValidationMode `json:"validationMode,omitempty"`
	StateMode      domain.StateMode      `json:"stateMode,omitempty"`
	Auth           *domain.AuthConfig    `json:"auth,omitempty"`
}

// BundleContract is one contract version in a bundle, oldest first.
type BundleContract struct {
	Format    string    `json:"format"`
	Raw       string    `json:"raw"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
}

// ImportResult says what an import created.
type ImportResult struct {
	Project   *domain.Project `json:"project"`
	Kind      string          `json:"kind"`
	Contracts int             `json:"contracts"`
	Endpoints int             `json:"endpoints"`
	Mocks     int             `json:"mocks"`
	Warnings  []string        `json:"warnings"`
}

// ExchangeService imports and exports projects.
type ExchangeService struct {
	projects  *ProjectService
	contracts ContractRepository
	service   *ContractService
	mocks     *MockService
	codec     ExchangeCodec
}

func NewExchangeService(
	projects *ProjectService,
	contracts ContractRepository,
	contractService *ContractService,
	mocks *MockService,
	codec ExchangeCodec,
) *ExchangeService {
	return &ExchangeService{
		projects: projects, contracts: contracts, service: contractService, mocks: mocks, codec: codec,
	}
}

// Import creates a new project from a file: an openapi-mocker bundle, a
// Postman collection, an Insomnia export, a HAR file or an OpenAPI / Swagger
// contract. name overrides the project name; "" takes it from the file.
func (s *ExchangeService) Import(data []byte, name string) (*ImportResult, error) {
	kind, err := s.codec.DetectImport(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidImport, err)
	}

	var result *ImportResult

	switch kind {
	case KindBundle:
		result, err = s.importBundle(data, name)
	case KindOpenAPI:
		result, err = s.importContract(data, name)
	default:
		result, err = s.importCollection(kind, data, name)
	}

	if err != nil {
		return nil, err
	}

	result.Kind = kind

	return result, nil
}

// createProject makes the project an import fills; fail removes it again when
// the import cannot be completed, so no half-imported project stays behind.
func (s *ExchangeService) createProject(name, description, fallback string) (*domain.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}

	return s.projects.Create(name, description)
}

func (s *ExchangeService) fail(projectID string, err error) error {
	_ = s.projects.Delete(projectID)

	return err
}

func (s *ExchangeService) importContract(data []byte, name string) (*ImportResult, error) {
	p, err := s.createProject(name, "", "Imported contract")
	if err != nil {
		return nil, err
	}

	c, err := s.service.Publish(p.ID, string(data), "import")
	if err != nil {
		return nil, s.fail(p.ID, fmt.Errorf("%w: %w", ErrInvalidImport, err))
	}

	endpoints, _ := s.service.ListEndpoints(p.ID)

	res := &ImportResult{Project: p, Kind: "", Contracts: 1, Endpoints: len(endpoints), Mocks: 0, Warnings: nil}
	if _, note, ok := strings.Cut(c.Source, " ("); ok && strings.Contains(note, "converted") {
		res.Warnings = append(res.Warnings, strings.TrimSuffix(note, ")"))
	}

	return res, nil
}
