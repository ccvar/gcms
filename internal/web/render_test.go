package web

import (
	"os"
	"testing"
)

func TestRendererParsesTemplates(t *testing.T) {
	if _, err := NewRenderer(os.DirFS("../..")); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
}
