//go:build !unix

package ipmap

import "os"

// mapFile on platforms without a usable mmap reads the whole artifact into
// memory: correct everywhere, at the cost of a copy the unix path avoids.
func mapFile(path string) ([]byte, func() error, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return data, func() error { return nil }, nil
}
