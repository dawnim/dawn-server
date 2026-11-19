package codegen

import (
	"os"
	"path/filepath"
	"slices"
)

// Write creates generated files under output without replacing existing source.
// A failed write removes only the files created by this call.
func Write(output string, files map[string][]byte) ([]string, error) {
	if err := os.MkdirAll(output, 0750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(output)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	created := []string{}
	for name, data := range files {
		if err = root.MkdirAll(filepath.Dir(name), 0750); err != nil {
			break
		}
		var file *os.File
		file, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
		if err != nil {
			break
		}
		created = append(created, name)
		_, err = file.Write(data)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		for _, name := range created {
			_ = root.Remove(name)
		}
		return nil, err
	}
	slices.Sort(created)
	return created, nil
}
