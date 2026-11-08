package protobuf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
)

const shopProto = `
// A shop.
syntax = "proto3";
package shop.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";
import "google/protobuf/empty.proto";

option go_package = "example.com/shop";

enum Status { STATUS_UNSPECIFIED = 0; ACTIVE = 1; DELETED = 2; }

message Money { string currency = 1; int64 units = 2; sint32 nanos = 3; }

message Item {
  string id = 1;
  string name = 2 [json_name = "itemName"];
  Status status = 3;
  repeated string tags = 4;
  repeated int32 counts = 5;
  map<string, int32> stock = 6;
  map<string, Money> prices = 7;
  Money price = 8;
  bytes blob = 9;
  google.protobuf.Timestamp created_at = 10;
  optional string note = 11;
  oneof kind { string sku = 12; int32 legacy_id = 13; }
  double weight = 14;
  bool in_stock = 15;
  fixed32 f32 = 16;
  sfixed64 s64 = 17;
  uint64 big = 18;
  google.protobuf.Int32Value maybe = 19;
  Nested nested = 20;
  message Nested { repeated Item children = 1; Inner inner = 2; message Inner { string v = 1; } }
  reserved 100 to 110;
}

message GetItemRequest { string id = 1; }
message ListItemsRequest { int32 page_size = 1; }

service Shop {
  rpc GetItem (GetItemRequest) returns (Item);
  rpc ListItems (ListItemsRequest) returns (stream Item) { option deprecated = true; }
  rpc Upload (stream Item) returns (google.protobuf.Empty);
  rpc Chat (stream Item) returns (stream Item);
}
`

func parse(t *testing.T) *protobuf.Registry {
	t.Helper()

	r, err := protobuf.Parse(shopProto)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func TestParse_Services(t *testing.T) {
	t.Parallel()

	r := parse(t)

	svcs := r.Services()
	if len(svcs) != 1 || svcs[0].FullName != "shop.v1.Shop" || len(svcs[0].Methods) != 4 {
		t.Fatalf("services: %+v", svcs)
	}

	want := map[string][2]bool{"GetItem": {false, false}, "ListItems": {false, true}, "Upload": {true, false}, "Chat": {true, true}}
	for _, m := range svcs[0].Methods {
		if got := [2]bool{m.ClientStreaming, m.ServerStreaming}; got != want[m.Name] {
			t.Errorf("%s: streaming %v, want %v", m.Name, got, want[m.Name])
		}
	}

	if p := svcs[0].Methods[0].Path(svcs[0]); p != "/shop.v1.Shop/GetItem" {
		t.Errorf("path %q", p)
	}

	if r.Message("shop.v1.Item.Nested.Inner") == nil {
		t.Error("nested message not registered")
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"unknown type":      `syntax="proto3"; message A { Missing m = 1; }`,
		"dup number":        `syntax="proto3"; message A { int32 a = 1; int32 b = 1; }`,
		"dup name":          `syntax="proto3"; message A { int32 a = 1; int32 a = 2; }`,
		"bad number":        `syntax="proto3"; message A { int32 a = 0; }`,
		"reserved number":   `syntax="proto3"; message A { int32 a = 19500; }`,
		"foreign import":    `syntax="proto3"; import "other.proto";`,
		"unknown rpc type":  `syntax="proto3"; message A {} service S { rpc M (A) returns (Nope); }`,
		"enum first":        `syntax="proto3"; enum E { A = 1; }`,
		"unterminated":      `syntax="proto3"; message A { int32 a = 1;`,
		"bad map key":       `syntax="proto3"; message A { map<double,int32> m = 1; }`,
		"bad syntax":        `syntax="proto9";`,
		"extend":            `syntax="proto2"; extend Foo { optional int32 x = 100; }`,
		"unterminated str":  `syntax="proto3`,
		"unterminated cmt":  `/* nope`,
		"duplicate methods": `syntax="proto3"; message A {} service S { rpc M (A) returns (A); rpc M (A) returns (A); }`,
	}

	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := protobuf.Parse(src)
			if err == nil || !errors.Is(err, protobuf.ErrSyntax) {
				t.Errorf("want ErrSyntax, got %v", err)
			}
		})
	}
}

func TestMarshal_KnownBytes(t *testing.T) {
	t.Parallel()

	r, err := protobuf.Parse(`syntax="proto3"; message M { int32 a = 1; string b = 2; }`)
	if err != nil {
		t.Fatal(err)
	}

	got, err := protobuf.Marshal(r.Message("M"), []byte(`{"a":150,"b":"testing"}`))
	if err != nil {
		t.Fatal(err)
	}

	// The canonical example of the protobuf encoding guide plus a string field.
	want := []byte{0x08, 0x96, 0x01, 0x12, 0x07, 't', 'e', 's', 't', 'i', 'n', 'g'}
	if !bytes.Equal(got, want) {
		t.Errorf("got % x, want % x", got, want)
	}

	// Defaults are not written.
	got, _ = protobuf.Marshal(r.Message("M"), []byte(`{"a":0,"b":""}`))
	if len(got) != 0 {
		t.Errorf("zero values must not be encoded, got % x", got)
	}
}

func roundTrip(t *testing.T, r *protobuf.Registry, msg, in string) map[string]any {
	t.Helper()

	m := r.Message(msg)

	wire, err := protobuf.Marshal(m, []byte(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, err := protobuf.Unmarshal(m, wire)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var v map[string]any

	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()

	if err = dec.Decode(&v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}

	return v
}

func TestRoundTrip_Item(t *testing.T) {
	t.Parallel()

	r := parse(t)

	in := `{
	  "id":"i-1","itemName":"Lamp","status":"ACTIVE","tags":["a","b"],"counts":[1,2,300],
	  "stock":{"x":5,"y":0},"prices":{"eu":{"currency":"EUR","units":"12","nanos":-5}},
	  "price":{"currency":"USD","units":"9007199254740993"},"blob":"aGVsbG8=",
	  "createdAt":"2024-05-06T07:08:09.5Z","note":"","sku":"SKU-9","weight":2.5,"inStock":true,
	  "f32":4000000000,"s64":"-7","big":"18446744073709551615","maybe":42,
	  "nested":{"children":[{"id":"c"}],"inner":{"v":"deep"}}
	}`

	v := roundTrip(t, r, "shop.v1.Item", in)

	checks := map[string]any{
		"id": "i-1", "itemName": "Lamp", "status": "ACTIVE", "blob": "aGVsbG8=",
		"createdAt": "2024-05-06T07:08:09.5Z", "note": "", "sku": "SKU-9", "inStock": true,
		"s64": "-7", "big": "18446744073709551615",
	}
	for k, want := range checks {
		if v[k] != want {
			t.Errorf("%s = %#v, want %#v", k, v[k], want)
		}
	}

	if got := v["price"].(map[string]any)["units"]; got != "9007199254740993" {
		t.Errorf("int64 precision lost: %v", got)
	}

	if got := v["prices"].(map[string]any)["eu"].(map[string]any)["nanos"]; got.(json.Number).String() != "-5" {
		t.Errorf("sint32: %v", got)
	}

	if got := v["stock"].(map[string]any); len(got) != 2 || got["y"].(json.Number).String() != "0" {
		t.Errorf("map with a zero value: %v", got)
	}

	if got := v["counts"].([]any); len(got) != 3 || got[2].(json.Number).String() != "300" {
		t.Errorf("packed ints: %v", got)
	}

	if v["maybe"].(json.Number).String() != "42" {
		t.Errorf("wrapper: %v", v["maybe"])
	}

	if v["f32"].(json.Number).String() != "4000000000" {
		t.Errorf("fixed32: %v", v["f32"])
	}

	if _, has := v["legacyId"]; has {
		t.Error("an unset oneof member must be omitted")
	}

	nested := v["nested"].(map[string]any)
	if nested["inner"].(map[string]any)["v"] != "deep" || len(nested["children"].([]any)) != 1 {
		t.Errorf("nested: %v", nested)
	}
}

func TestUnmarshal_DefaultsAndUnknownFields(t *testing.T) {
	t.Parallel()

	r := parse(t)
	m := r.Message("shop.v1.Item")

	// An unknown field (99, varint) is skipped; absent fields get defaults.
	out, err := protobuf.Unmarshal(m, []byte{0x98, 0x06, 0x01})
	if err != nil {
		t.Fatal(err)
	}

	var v map[string]any
	if err = json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}

	if v["id"] != "" || v["status"] != "STATUS_UNSPECIFIED" || v["weight"].(float64) != 0 || v["big"] != "0" {
		t.Errorf("defaults: %v", v)
	}

	for _, absent := range []string{"price", "note", "sku", "maybe", "createdAt"} {
		if _, has := v[absent]; has {
			t.Errorf("%s must be omitted when unset", absent)
		}
	}

	if _, has := v["tags"]; !has {
		t.Error("repeated fields are always present")
	}
}

func TestMarshal_Errors(t *testing.T) {
	t.Parallel()

	r := parse(t)
	m := r.Message("shop.v1.Item")

	for name, in := range map[string]string{
		"not json":     `{`,
		"unknown":      `{"nope":1}`,
		"wrong type":   `{"id":5}`,
		"bad enum":     `{"status":"WAT"}`,
		"int overflow": `{"counts":[4294967296]}`,
		"two oneofs":   `{"sku":"a","legacyId":1}`,
		"bad bytes":    `{"blob":"!!!"}`,
		"bad time":     `{"createdAt":"yesterday"}`,
		"not object":   `[]`,
		"trailing":     `{} {}`,
		"array needed": `{"tags":"x"}`,
		"map value":    `{"stock":{"a":null}}`,
	} {
		_, err := protobuf.Marshal(m, []byte(in))
		if !errors.Is(err, protobuf.ErrJSON) {
			t.Errorf("%s: want ErrJSON, got %v", name, err)
		}
	}
}

func TestUnmarshal_Malformed(t *testing.T) {
	t.Parallel()

	m := parse(t).Message("shop.v1.Item")

	for name, data := range map[string][]byte{
		"truncated varint": {0x08, 0x80},
		"bad length":       {0x12, 0x09, 'a'},
		"field zero":       {0x00, 0x01},
		"wrong wire type":  {0x0a, 0x01, 'x', 0x10, 0x01, 0x1a, 0x00}, // field 3 (enum) fine; below breaks
	} {
		_, err := protobuf.Unmarshal(m, data)
		if name != "wrong wire type" && !errors.Is(err, protobuf.ErrWire) {
			t.Errorf("%s: want ErrWire, got %v", name, err)
		}
	}

	// A string field carried as a varint.
	if _, err := protobuf.Unmarshal(m, []byte{0x0a - 2 + 0, 0x01}); !errors.Is(err, protobuf.ErrWire) {
		t.Errorf("string as varint: %v", err)
	}
}

func TestWellKnown_Conversions(t *testing.T) {
	t.Parallel()

	r, err := protobuf.Parse(`syntax="proto3"; import "google/protobuf/duration.proto"; import "google/protobuf/struct.proto";
import "google/protobuf/field_mask.proto"; import "google/protobuf/wrappers.proto";
message W { google.protobuf.Duration d = 1; google.protobuf.Struct s = 2; google.protobuf.Value v = 3;
 google.protobuf.FieldMask m = 4; google.protobuf.StringValue sv = 5; google.protobuf.ListValue l = 6; }`)
	if err != nil {
		t.Fatal(err)
	}

	in := `{"d":"-1.5s","s":{"a":1,"b":["x",true,null],"c":{"d":"e"}},"v":"str","m":"fooBar,baz","sv":"hi","l":[1,"two"]}`

	v := roundTrip(t, r, "W", in)

	if v["d"] != "-1.5s" || v["v"] != "str" || v["m"] != "fooBar,baz" || v["sv"] != "hi" {
		t.Errorf("scalars: %v", v)
	}

	s := v["s"].(map[string]any)
	if s["a"].(json.Number).String() != "1" || len(s["b"].([]any)) != 3 || s["c"].(map[string]any)["d"] != "e" {
		t.Errorf("struct: %v", s)
	}

	if l := v["l"].([]any); len(l) != 2 || l[1] != "two" {
		t.Errorf("list: %v", l)
	}
}

func TestExample_ValidAndStable(t *testing.T) {
	t.Parallel()

	r := parse(t)

	for _, name := range []string{"shop.v1.Item", "shop.v1.GetItemRequest", "shop.v1.Money"} {
		m := r.Message(name)
		ex := protobuf.Example(m)

		if !json.Valid(ex) {
			t.Fatalf("%s: example is not JSON: %s", name, ex)
		}

		// The example must be accepted by the codec.
		wire, err := protobuf.Marshal(m, ex)
		if err != nil {
			t.Fatalf("%s: example does not marshal: %v\n%s", name, err, ex)
		}

		if _, err = protobuf.Unmarshal(m, wire); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if !bytes.Equal(ex, protobuf.Example(m)) {
			t.Errorf("%s: example is not deterministic", name)
		}
	}

	ex := string(protobuf.Example(r.Message("shop.v1.Item")))
	for _, want := range []string{`"itemName":"name"`, `"status":"ACTIVE"`, `"stock":{"key":1}`, `"createdAt":"2024-01-01T00:00:00Z"`} {
		if !strings.Contains(ex, want) {
			t.Errorf("example lacks %s:\n%s", want, ex)
		}
	}
}

func TestExample_RecursiveMessage(t *testing.T) {
	t.Parallel()

	r, err := protobuf.Parse(`syntax="proto3"; message Node { string v = 1; Node next = 2; repeated Node kids = 3; }`)
	if err != nil {
		t.Fatal(err)
	}

	ex := protobuf.Example(r.Message("Node"))
	if !json.Valid(ex) || len(ex) > 1000 {
		t.Errorf("recursive example: %d bytes, valid=%v", len(ex), json.Valid(ex))
	}
}

func TestDescriptors(t *testing.T) {
	t.Parallel()

	r := parse(t)

	fd, err := r.FileDescriptor("service.proto")
	if err != nil {
		t.Fatal(err)
	}

	// The descriptor mentions the package, a service, a method and a map entry type.
	for _, want := range []string{"shop.v1", "Shop", "GetItem", "StockEntry", "PricesEntry", ".shop.v1.Item", "google/protobuf/timestamp.proto"} {
		if !bytes.Contains(fd, []byte(want)) {
			t.Errorf("descriptor lacks %q", want)
		}
	}

	for symbol, file := range map[string]string{
		"shop.v1.Shop":              "service.proto",
		"shop.v1.Shop.GetItem":      "service.proto",
		"shop.v1.Item":              "service.proto",
		"shop.v1.Status":            "service.proto",
		"google.protobuf.Timestamp": "google/protobuf/timestamp.proto",
	} {
		got, ok := r.FileContaining(symbol)
		if !ok || got != file {
			t.Errorf("FileContaining(%s) = %q, %v", symbol, got, ok)
		}
	}

	if _, ok := r.FileContaining("nope.X"); ok {
		t.Error("an unknown symbol must not be found")
	}

	if _, err = r.FileDescriptor("missing.proto"); err == nil {
		t.Error("an unknown file must fail")
	}

	if _, err = r.FileDescriptor("google/protobuf/struct.proto"); err != nil {
		t.Error(err)
	}
}
