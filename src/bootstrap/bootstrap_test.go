package bootstrap

import (
	"testing"

	"go.uber.org/fx"
)

// TestAppGraphIsValid checks that every dependency the application declares can
// actually be satisfied. fx resolves the graph at startup, so a missing or
// duplicated provider is otherwise only discovered by running the service
// against real infrastructure. ValidateApp constructs nothing and connects to
// nothing, so this stays a unit test.
func TestAppGraphIsValid(t *testing.T) {
	if err := fx.ValidateApp(NewApp()); err != nil {
		t.Fatalf("application graph is not satisfiable: %v", err)
	}
}
