//go:build !darwin && !linux

package factorycli

import "errors"

func replaceProcess(string, []string, []string) error {
	return errors.New("Factory process commands require macOS or Linux")
}
