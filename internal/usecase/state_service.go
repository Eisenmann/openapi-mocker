package usecase

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Errors of the stateful CRUD behavior; they never leave the package, they
// are turned into 4xx mock responses.
var (
	errStateNotFound = errors.New("resource not found")
	errStateConflict = errors.New("a resource with this id already exists")
	errStateBody     = errors.New("request body must be a JSON object")
)

const (
	statusCreated    = 201
	statusNoContent  = 204
	statusConflict   = 409
	uuidRandomBytes  = 16
	uuidVersionByte  = 6
	uuidVariantByte  = 8
	uuidVersionMask  = 0x0f
	uuidVersionValue = 0x40
	uuidVariantMask  = 0x3f
	uuidVariantValue = 0x80
)

// StateService makes POST, PUT, PATCH and DELETE change the data that later
// GETs return. Resources live in collections keyed by the collection URL
// (e.g. /users); the conventions are inferred from the contract's paths:
//
//	GET    /users        list the collection
//	POST   /users        create a resource (id taken from the body or generated)
//	GET    /users/{id}   read one resource, 404 when missing
//	PUT    /users/{id}   replace a resource, 404 when missing
//	PATCH  /users/{id}   merge fields into a resource, 404 when missing
//	DELETE /users/{id}   remove a resource, 404 when missing
//
// Operations that do not fit the conventions are left to the static mock.
type StateService struct {
	store    StateStore
	projects ProjectGetter
	engine   ContractEngine
}

func NewStateService(store StateStore, projects ProjectGetter, engine ContractEngine) *StateService {
	return &StateService{store: store, projects: projects, engine: engine}
}

// stateTarget is what a request path addresses: a collection, or one item.
type stateTarget struct {
	collection string
	id         string
	isItem     bool
}

// classify splits a request path by its contract template: a template ending
// in a {param} segment addresses one item of the collection before it.
func classify(pathTemplate, path string) stateTarget {
	path = "/" + strings.Trim(path, "/")
	tpl := strings.TrimRight(pathTemplate, "/")

	last := tpl[strings.LastIndex(tpl, "/")+1:]
	if !strings.HasPrefix(last, "{") || !strings.HasSuffix(last, "}") {
		return stateTarget{collection: path}
	}

	cut := strings.LastIndex(path, "/")

	return stateTarget{collection: path[:cut], id: path[cut+1:], isItem: true}
}

func normalizeCollection(name string) string {
	return "/" + strings.Trim(name, "/")
}

// mode returns the project's state mode (off when it cannot be loaded).
func (s *StateService) mode(projectID string) domain.StateMode {
	p, err := s.projects.Get(projectID)
	if err != nil || !p.StateMode.Enabled() {
		return domain.StateOff
	}

	return p.StateMode
}

// Handle serves the request from the project's collections. ok is false when
// state is off for the project or the operation does not follow the CRUD
// conventions, so the caller falls back to the static mock.
func (s *StateService) Handle(projectID, raw, pathTemplate string, req *MockRequest) (resp *MockResponse, ok bool) {
	mode := s.mode(projectID)
	if !mode.Enabled() {
		return nil, false
	}

	t := classify(pathTemplate, req.Path)
	op := stateOp{svc: s, projectID: projectID, mode: mode, raw: raw, req: req, target: t}
	method := strings.ToUpper(req.Method)

	if t.isItem {
		return op.item(method)
	}

	return op.collection(method)
}

// stateOp bundles what one stateful request needs.
type stateOp struct {
	svc       *StateService
	projectID string
	mode      domain.StateMode
	raw       string
	req       *MockRequest
	target    stateTarget
}

func (o *stateOp) collection(method string) (*MockResponse, bool) {
	switch method {
	case "GET":
		return o.list(), true
	case "POST":
		return o.create(), true
	default:
		return nil, false
	}
}

func (o *stateOp) item(method string) (*MockResponse, bool) {
	switch method {
	case "GET":
		return o.read(), true
	case "PUT", "PATCH":
		return o.update(method == "PATCH"), true
	case "DELETE":
		return o.remove(), true
	default:
		return nil, false
	}
}

func (o *stateOp) status(def int) int {
	code, ok := o.svc.engine.SuccessStatus([]byte(o.raw), strings.ToUpper(o.req.Method), o.req.Path)
	if !ok {
		return def
	}

	return code
}

func (o *stateOp) list() *MockResponse {
	c := o.svc.store.View(o.projectID, o.mode, o.target.collection)

	parts := make([][]byte, 0, len(c.Items))
	for i := range c.Items {
		parts = append(parts, c.Items[i].Data)
	}

	body := append([]byte("["), bytes.Join(parts, []byte(","))...)
	body = append(body, ']')

	return stateResponse(StatusOK, body)
}

func (o *stateOp) read() *MockResponse {
	c := o.svc.store.View(o.projectID, o.mode, o.target.collection)

	i := indexOf(&c, o.target.id)
	if i < 0 {
		return o.notFound()
	}

	return stateResponse(StatusOK, c.Items[i].Data)
}

func (o *stateOp) create() *MockResponse {
	obj, err := parseObject(o.req.Body)
	if err != nil {
		return stateFailure(StatusBadRequest, err)
	}

	idIsString := o.idIsString()

	var item domain.StateItem

	err = o.svc.store.Transact(o.projectID, o.mode, o.target.collection, func(c *domain.StateCollection) error {
		var addErr error

		item, addErr = addItem(c, obj, idIsString)

		return addErr
	})
	if err != nil {
		return o.failure(err)
	}

	return stateResponse(o.status(statusCreated), item.Data)
}

func (o *stateOp) update(merge bool) *MockResponse {
	obj, err := parseObject(o.req.Body)
	if err != nil {
		return stateFailure(StatusBadRequest, err)
	}

	var item domain.StateItem

	err = o.svc.store.Transact(o.projectID, o.mode, o.target.collection, func(c *domain.StateCollection) error {
		i := indexOf(c, o.target.id)
		if i < 0 {
			return errStateNotFound
		}

		next, buildErr := buildUpdate(&c.Items[i], obj, merge)
		if buildErr != nil {
			return buildErr
		}

		c.Items[i] = next
		item = next

		return nil
	})
	if err != nil {
		return o.failure(err)
	}

	return stateResponse(o.status(StatusOK), item.Data)
}

func (o *stateOp) remove() *MockResponse {
	err := o.svc.store.Transact(o.projectID, o.mode, o.target.collection, func(c *domain.StateCollection) error {
		i := indexOf(c, o.target.id)
		if i < 0 {
			return errStateNotFound
		}

		c.Items = append(c.Items[:i], c.Items[i+1:]...)

		return nil
	})
	if err != nil {
		return o.failure(err)
	}

	code := o.status(statusNoContent)
	if code == statusNoContent {
		return &MockResponse{StatusCode: code, Source: "state", Matched: true}
	}

	return stateResponse(code, []byte(`{}`))
}

// failure maps a store-transaction error to a mock response.
func (o *stateOp) failure(err error) *MockResponse {
	switch {
	case errors.Is(err, errStateNotFound):
		return o.notFound()
	case errors.Is(err, errStateConflict):
		return stateFailure(statusConflict, err)
	default:
		return stateFailure(StatusInternalServerError, err)
	}
}

func (o *stateOp) notFound() *MockResponse {
	return stateFailure(StatusNotFound,
		fmt.Errorf("%w: %s/%s", errStateNotFound, o.target.collection, o.target.id))
}

// idIsString reports whether the contract declares the created resource's id
// as a string (generate a UUID) rather than an integer (sequential).
func (o *stateOp) idIsString() bool {
	status := strconv.Itoa(o.status(statusCreated))

	schema, err := o.svc.engine.ResponseSchemaJSON([]byte(o.raw), "POST", o.req.Path, status)
	if err != nil {
		return false
	}

	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}

	if json.Unmarshal([]byte(schema), &s) != nil {
		return false
	}

	return s.Properties["id"].Type == "string"
}

func stateResponse(code int, body []byte) *MockResponse {
	return &MockResponse{
		StatusCode: code, ContentType: ContentTypeJSON, Body: body,
		Source: "state", Matched: true,
	}
}

func stateFailure(code int, err error) *MockResponse {
	return stateResponse(code, errorBody(err.Error()))
}

// ---------- Item helpers ----------.

func indexOf(c *domain.StateCollection, id string) int {
	for i := range c.Items {
		if c.Items[i].ID == id {
			return i
		}
	}

	return -1
}

// parseObject decodes a request body as a JSON object, keeping numbers exact.
func parseObject(body []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	var obj map[string]any

	err := dec.Decode(&obj)
	if err != nil || obj == nil {
		return nil, errStateBody
	}

	return obj, nil
}

// addItem stores obj in c. The id comes from obj["id"] when present;
// otherwise a UUID (string ids) or the next sequential integer is generated.
func addItem(c *domain.StateCollection, obj map[string]any, idIsString bool) (domain.StateItem, error) {
	if c.NextID < 1 {
		c.NextID = 1
	}

	if v, ok := obj["id"]; ok && v != nil {
		bumpNextID(c, v)
	} else if idIsString {
		obj["id"] = newUUID()
	} else {
		for indexOf(c, strconv.Itoa(c.NextID)) >= 0 {
			c.NextID++
		}

		obj["id"] = json.Number(strconv.Itoa(c.NextID))
		c.NextID++
	}

	id := fmt.Sprint(obj["id"])
	if indexOf(c, id) >= 0 {
		return domain.StateItem{}, fmt.Errorf("%w: %s", errStateConflict, id)
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return domain.StateItem{}, fmt.Errorf("encode resource: %w", err)
	}

	item := domain.StateItem{ID: id, Data: data}
	c.Items = append(c.Items, item)

	return item, nil
}

// bumpNextID keeps sequential ids ahead of explicitly supplied integer ids.
func bumpNextID(c *domain.StateCollection, v any) {
	n, ok := v.(json.Number)
	if !ok {
		return
	}

	i, err := strconv.Atoi(n.String())
	if err == nil && i >= c.NextID {
		c.NextID = i + 1
	}
}

// buildUpdate returns the item after a PUT (replace) or PATCH (merge). The id
// of the stored item always survives.
func buildUpdate(old *domain.StateItem, obj map[string]any, merge bool) (domain.StateItem, error) {
	var stored map[string]any

	dec := json.NewDecoder(bytes.NewReader(old.Data))
	dec.UseNumber()

	err := dec.Decode(&stored)
	if err != nil {
		return domain.StateItem{}, fmt.Errorf("decode stored resource: %w", err)
	}

	next := obj
	if merge {
		next = stored
		for k, v := range obj {
			next[k] = v
		}
	}

	next["id"] = stored["id"]

	data, err := json.Marshal(next)
	if err != nil {
		return domain.StateItem{}, fmt.Errorf("encode resource: %w", err)
	}

	return domain.StateItem{ID: old.ID, Data: data}, nil
}

func newUUID() string {
	b := make([]byte, uuidRandomBytes)

	_, err := rand.Read(b)
	if err != nil {
		panic(err) // crypto/rand does not fail on supported platforms.
	}

	b[uuidVersionByte] = b[uuidVersionByte]&uuidVersionMask | uuidVersionValue
	b[uuidVariantByte] = b[uuidVariantByte]&uuidVariantMask | uuidVariantValue

	h := hex.EncodeToString(b)

	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// ---------- Administration (used by the HTTP admin API) ----------.

// Snapshot returns the project's collections as arrays of resources.
func (s *StateService) Snapshot(projectID string) (map[string][]json.RawMessage, error) {
	mode := s.mode(projectID)
	if !mode.Enabled() {
		return nil, ErrStateNotEnabled
	}

	snap := s.store.Snapshot(projectID, mode)
	out := make(map[string][]json.RawMessage, len(snap))

	for name, c := range snap {
		items := make([]json.RawMessage, 0, len(c.Items))
		for i := range c.Items {
			items = append(items, c.Items[i].Data)
		}

		out[name] = items
	}

	return out, nil
}

// Seed replaces a collection with the given JSON array of objects and
// returns how many resources it now holds.
func (s *StateService) Seed(projectID, collection string, body []byte) (int, error) {
	mode := s.mode(projectID)
	if !mode.Enabled() {
		return 0, ErrStateNotEnabled
	}

	var objs []map[string]any

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	err := dec.Decode(&objs)
	if err != nil || objs == nil {
		return 0, ErrSeedNotArray
	}

	c := domain.StateCollection{Items: make([]domain.StateItem, 0, len(objs)), NextID: 1}

	for _, obj := range objs {
		_, addErr := addItem(&c, obj, false)
		if addErr != nil {
			return 0, fmt.Errorf("%w: %w", ErrSeedInvalid, addErr)
		}
	}

	err = s.store.Replace(projectID, mode, normalizeCollection(collection), &c)
	if err != nil {
		return 0, fmt.Errorf("seed collection: %w", err)
	}

	return len(c.Items), nil
}

// Reset empties one collection, or every collection of the project when
// collection is empty. It works in any mode, so leftovers can be cleaned up.
func (s *StateService) Reset(projectID, collection string) error {
	if collection != "" {
		collection = normalizeCollection(collection)
	}

	err := s.store.Reset(projectID, collection)
	if err != nil {
		return fmt.Errorf("reset state: %w", err)
	}

	return nil
}
