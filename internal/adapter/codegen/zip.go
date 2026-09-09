package codegen

import (
	"archive/zip"
	"bytes"
)

func toZip(files map[string]string) ([]byte, error) {
	buf := &bytes.Buffer{}

	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}

		_, err = w.Write([]byte(content))
		if err != nil {
			return nil, err
		}
	}

	err := zw.Close()
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
