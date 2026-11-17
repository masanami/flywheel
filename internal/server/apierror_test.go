package server

import (
	"os"
	"strings"
	"testing"
)

// API が定義するエラーコードの一覧（apierror.go）に serialized が無い。
func TestAPIErrorCodes_HaveNoSerialized(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("apierror.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"serialized"`) {
		t.Error(`apierror.go still defines the error code "serialized"`)
	}
}
