package protobuf

import (
	"fmt"
	"strings"
)

// defaultProtoFile names the file of a contract that is a single .proto.
const defaultProtoFile = "service.proto"

// srcFile is one .proto file of a contract.
type srcFile struct {
	name string
	text string
}

// fileMarkerKey is the word that, after "//" and optional "=" signs,
// introduces a file in a multi-file contract.
const fileMarkerKey = "file:"

// fileMarker returns the file name of a marker line such as
//
//	// ==== file: common/types.proto ====
//
// and false for any other line.
func fileMarker(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "//") {
		return "", false
	}

	t = strings.TrimSpace(strings.Trim(strings.TrimPrefix(t, "//"), "= \t"))
	if !strings.HasPrefix(t, fileMarkerKey) {
		return "", false
	}

	return strings.TrimSpace(strings.TrimPrefix(t, fileMarkerKey)), true
}

// splitFiles splits a contract into its files. A contract is one .proto, or
// several in one text, each after a marker line:
//
//	// ==== file: common/types.proto ====
//	syntax = "proto3"; ...
//	// ==== file: shop/v1/shop.proto ====
//	import "common/types.proto"; ...
//
// Text before the first marker is a file of its own ("service.proto") unless
// it only holds blanks and comments.
func splitFiles(src string) ([]srcFile, error) {
	var (
		files []srcFile
		cur   = srcFile{name: defaultProtoFile, text: ""}
		body  []string
		seen  = map[string]bool{}
	)

	flush := func(isFirst bool) error {
		cur.text = strings.Join(body, "\n")
		body = body[:0]

		if isFirst && !hasTokens(cur.text) {
			return nil
		}

		if seen[cur.name] {
			return fmt.Errorf("%w: file %q appears twice", ErrSyntax, cur.name)
		}

		seen[cur.name] = true
		files = append(files, cur)

		return nil
	}

	first := true

	for _, line := range strings.Split(src, "\n") {
		name, ok := fileMarker(line)
		if !ok {
			body = append(body, line)

			continue
		}

		err := validateFileName(name)
		if err != nil {
			return nil, err
		}

		err = flush(first)
		if err != nil {
			return nil, err
		}

		first = false
		cur = srcFile{name: name, text: ""}
	}

	err := flush(first)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return []srcFile{{name: defaultProtoFile, text: src}}, nil
	}

	return files, nil
}

// hasTokens reports whether text holds anything but blanks and comments.
func hasTokens(text string) bool {
	toks, err := lex(text)

	return err != nil || len(toks) > 1 // the lexer always ends with an EOF token.
}

func validateFileName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: a file marker needs a file name", ErrSyntax)
	case strings.HasPrefix(name, "google/protobuf/"):
		return fmt.Errorf("%w: %q is reserved for the well-known types", ErrSyntax, name)
	case strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || strings.Contains(name, ".."):
		return fmt.Errorf("%w: bad file name %q (use a relative path with /)", ErrSyntax, name)
	}

	return nil
}

// checkImports verifies that every import names a well-known type or a file
// of the contract, and that the imports have no cycle.
func checkImports(files []*File) error {
	byName := make(map[string]*File, len(files))
	for _, f := range files {
		byName[f.Name] = f
	}

	for _, f := range files {
		for _, imp := range f.Imports {
			if _, ok := byName[imp]; !ok && !isWellKnownFile(imp) {
				return fmt.Errorf("%w: %s: import %q not found: only google/protobuf well-known types and files "+
					"of this contract (add them after a \"// ==== file: %s ====\" line) can be imported",
					ErrSyntax, f.Name, imp, imp)
			}
		}
	}

	return findImportCycle(files, byName)
}

// findImportCycle reports an import cycle, which protoc rejects too.
func findImportCycle(files []*File, byName map[string]*File) error {
	const (
		visiting = 1
		done     = 2
	)

	state := map[string]int{}

	var visit func(f *File) error

	visit = func(f *File) error {
		switch state[f.Name] {
		case visiting:
			return fmt.Errorf("%w: import cycle through %s", ErrSyntax, f.Name)
		case done:
			return nil
		}

		state[f.Name] = visiting

		for _, imp := range f.Imports {
			dep, ok := byName[imp]
			if !ok {
				continue
			}

			err := visit(dep)
			if err != nil {
				return err
			}
		}

		state[f.Name] = done

		return nil
	}

	for _, f := range files {
		err := visit(f)
		if err != nil {
			return err
		}
	}

	return nil
}
