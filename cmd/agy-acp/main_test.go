package main

import (
	"strings"
	"testing"

	agy "github.com/gluonfield/agy-go"
)

func TestSelectAgentRejectsAPIKeyMode(t *testing.T) {
	store, err := agy.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = selectAgent("api-key", "agy", store)
	if err == nil || !strings.Contains(err.Error(), "unknown auth mode") {
		t.Fatalf("err = %v, want unsupported api-key mode", err)
	}
}
