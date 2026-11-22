//go:build !windows

package main

import "os"

func createConfigExportTemp(dir string) (*os.File, error) {
	return os.CreateTemp(dir, "masir-server-*.json")
}
