package memory

import "testing"

func TestRegistry(t *testing.T) {
	root := t.TempDir()

	reg, err := OpenRegistry(root, NamespaceProjects)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := reg.List(); err != nil || len(list) != 0 {
		t.Fatalf("expected empty registry, got %v (err %v)", list, err)
	}

	if err := reg.Upsert(RegistryEntry{
		ID:         "abc123456789",
		Source:     "github.com/x/y",
		Name:       "y",
		LocalPaths: []string{"/a"},
	}); err != nil {
		t.Fatal(err)
	}

	// Re-upsert with a blank source/name must preserve the originals and union paths.
	if err := reg.Upsert(RegistryEntry{
		ID:         "abc123456789",
		LocalPaths: []string{"/b", "/a"},
	}); err != nil {
		t.Fatal(err)
	}

	e, ok, err := reg.Get("abc123456789")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if e.Source != "github.com/x/y" {
		t.Errorf("source lost on merge: %q", e.Source)
	}
	if e.Name != "y" {
		t.Errorf("name lost on merge: %q", e.Name)
	}
	if len(e.LocalPaths) != 2 {
		t.Errorf("LocalPaths = %v, want 2 unioned", e.LocalPaths)
	}
	if e.LastSeen.IsZero() {
		t.Error("LastSeen not stamped")
	}

	// Reopen from disk: the entry must persist.
	reg2, err := OpenRegistry(root, NamespaceProjects)
	if err != nil {
		t.Fatal(err)
	}
	list, err := reg2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "abc123456789" {
		t.Errorf("persistence failed: %v", list)
	}

	if _, err := OpenRegistry(root, "bogus"); err == nil {
		t.Error("expected error for invalid namespace")
	}
}
