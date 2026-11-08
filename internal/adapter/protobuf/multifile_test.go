package protobuf_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
)

const multiFileProto = `
// ==== file: common/types.proto ====
syntax = "proto3";
package common;

import "google/protobuf/timestamp.proto";

message Money { string currency = 1; int64 units = 2; }
enum Tier { TIER_UNSPECIFIED = 0; GOLD = 1; }
message Stamp { google.protobuf.Timestamp at = 1; }

// ==== file: shop/v1/shop.proto ====
syntax = "proto3";
package shop.v1;

import "common/types.proto";

message Item { string id = 1; common.Money price = 2; common.Tier tier = 3; common.Stamp seen = 4; }
message GetItemRequest { string id = 1; }
service Shop { rpc GetItem (GetItemRequest) returns (Item); }
`

func TestMultiFile_ServicesAndRoundTrip(t *testing.T) {
	t.Parallel()

	e := protobuf.NewEngine()
	raw := []byte(multiFileProto)

	if res := e.Validate(raw); !res.Valid {
		t.Fatalf("invalid: %v", res.Errors)
	}

	svcs, err := e.Services(raw)
	if err != nil || len(svcs) != 1 || svcs[0].Name != "shop.v1.Shop" {
		t.Fatalf("services: %+v, %v", svcs, err)
	}

	doc := `{"id":"a","price":{"currency":"EUR","units":"5"},"tier":"GOLD","seen":{"at":"2026-01-02T03:04:05Z"}}`

	wire, err := e.JSONToWire(raw, "shop.v1.Item", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}

	back, err := e.WireToJSON(raw, "shop.v1.Item", wire)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{`"currency":"EUR"`, `"units":"5"`, `"tier":"GOLD"`, `"at":"2026-01-02T03:04:05Z"`} {
		if !strings.Contains(string(back), want) {
			t.Errorf("round trip lacks %s: %s", want, back)
		}
	}

	ex, err := e.Example(raw, "shop.v1.Item")
	if err != nil || !strings.Contains(string(ex), `"price"`) {
		t.Errorf("example: %s, %v", ex, err)
	}
}

func TestMultiFile_ReflectionNamesAndImports(t *testing.T) {
	t.Parallel()

	e := protobuf.NewEngine()
	raw := []byte(multiFileProto)

	name, deps, err := e.FileContaining(raw, "shop.v1.Shop")
	if err != nil || name != "shop/v1/shop.proto" || len(deps) != 1 || deps[0] != "common/types.proto" {
		t.Errorf("FileContaining service: %q %v %v", name, deps, err)
	}

	name, deps, err = e.FileContaining(raw, "common.Money")
	if err != nil || name != "common/types.proto" || len(deps) != 1 || deps[0] != "google/protobuf/timestamp.proto" {
		t.Errorf("FileContaining message: %q %v %v", name, deps, err)
	}

	for _, f := range []string{"common/types.proto", "shop/v1/shop.proto", "google/protobuf/timestamp.proto"} {
		fd, ferr := e.FileDescriptor(raw, f)
		if ferr != nil || len(fd) == 0 || !strings.Contains(string(fd), f) {
			t.Errorf("FileDescriptor(%s): %v", f, ferr)
		}
	}

	imports, err := e.FileImports(raw, "common/types.proto")
	if err != nil || len(imports) != 1 {
		t.Errorf("FileImports: %v %v", imports, err)
	}

	if _, err = e.FileImports(raw, "nope.proto"); err == nil {
		t.Error("an unknown file must be an error")
	}
}

func TestMultiFile_Errors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ src, want string }{
		"missing import": {
			"// ==== file: a.proto ====\nsyntax = \"proto3\"; import \"b.proto\"; message A {}",
			`import "b.proto" not found`,
		},
		"cycle": {
			"// ==== file: a.proto ====\nsyntax = \"proto3\"; import \"b.proto\"; message A {}\n" +
				"// ==== file: b.proto ====\nsyntax = \"proto3\"; import \"a.proto\"; message B {}",
			"import cycle",
		},
		"duplicate message": {
			"// ==== file: a.proto ====\nsyntax = \"proto3\"; package p; message A {}\n" +
				"// ==== file: b.proto ====\nsyntax = \"proto3\"; package p; message A {}",
			"defined more than once",
		},
		"duplicate file": {
			"// ==== file: a.proto ====\nsyntax = \"proto3\";\n// ==== file: a.proto ====\nsyntax = \"proto3\";",
			"appears twice",
		},
		"reserved name": {
			"// ==== file: google/protobuf/any.proto ====\nsyntax = \"proto3\";",
			"reserved",
		},
		"bad name": {
			"// ==== file: ../a.proto ====\nsyntax = \"proto3\";",
			"bad file name",
		},
		"empty name": {
			"// ==== file: ====\nsyntax = \"proto3\";",
			"needs a file name",
		},
		"syntax error is named": {
			"// ==== file: a.proto ====\nsyntax = \"proto3\"; message {",
			"a.proto: ",
		},
		"unknown type across files": {
			"// ==== file: a.proto ====\nsyntax = \"proto3\"; message A { Nope n = 1; }\n" +
				"// ==== file: b.proto ====\nsyntax = \"proto3\"; message B {}",
			"Nope",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := protobuf.Parse(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got %v", tc.want, err)
			}

			if err != nil && !errors.Is(err, protobuf.ErrSyntax) && !strings.Contains(err.Error(), "Nope") {
				t.Errorf("error is not a syntax error: %v", err)
			}
		})
	}
}

func TestMultiFile_SingleFileBehaviourUnchanged(t *testing.T) {
	t.Parallel()

	// Text before the first marker is a file of its own; a marker line inside a
	// single file contract that only holds comments does not create one.
	src := "syntax = \"proto3\"; package m; message Main {}\n// ==== file: x.proto ====\nsyntax = \"proto3\"; package x; message X {}"

	r, err := protobuf.Parse(src)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := r.FileContaining("m.Main"); !ok {
		t.Error("the unnamed leading file is lost")
	}

	r, err = protobuf.Parse("// just a comment\nsyntax = \"proto3\"; package s; message S {}")
	if err != nil {
		t.Fatal(err)
	}

	if name, _ := r.FileContaining("s.S"); name != "service.proto" {
		t.Errorf("single file named %q", name)
	}
}
