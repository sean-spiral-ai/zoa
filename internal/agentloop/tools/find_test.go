package builtintools

import "testing"

func TestBuildFDArgsDoesNotRestrictToRegularFiles(t *testing.T) {
	args := buildFDArgs("*SwimLife*", ".")
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--type" && args[i+1] == "f" {
			t.Fatalf("expected fd args to allow directories, got %#v", args)
		}
	}
}

func TestBuildFindArgsDoesNotRestrictToRegularFiles(t *testing.T) {
	args := buildFindArgs("*SwimLife*", ".")
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-type" && args[i+1] == "f" {
			t.Fatalf("expected find args to allow directories, got %#v", args)
		}
	}
}
