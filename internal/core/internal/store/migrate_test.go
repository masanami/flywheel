package store

import (
	"testing"
	"testing/fstest"
)

func TestMigrations_ParsesEmbeddedProductionMigrations(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("Migrations() returned no migrations")
	}
	if migrations[0].Version != 1 {
		t.Fatalf("first migration version = %d, want 1", migrations[0].Version)
	}
	if migrations[0].SQL == "" {
		t.Fatal("first migration SQL is empty")
	}
}

func TestParseMigrations_OrdersByVersionAscending(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0002_second.sql": &fstest.MapFile{Data: []byte("-- second")},
		"migrations/0001_first.sql":  &fstest.MapFile{Data: []byte("-- first")},
	}
	migrations, err := parseMigrations(fsys, "migrations")
	if err != nil {
		t.Fatalf("parseMigrations() error = %v", err)
	}
	if len(migrations) != 2 {
		t.Fatalf("len(migrations) = %d, want 2", len(migrations))
	}
	if migrations[0].Version != 1 || migrations[1].Version != 2 {
		t.Fatalf("migrations not ordered by version: %+v", migrations)
	}
}

func TestParseMigrations_RejectsDuplicateVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("-- a")},
		"migrations/0001_b.sql": &fstest.MapFile{Data: []byte("-- b")},
	}
	if _, err := parseMigrations(fsys, "migrations"); err == nil {
		t.Fatal("parseMigrations() with duplicate version should error, got nil")
	}
}

func TestParseMigrations_RejectsMissingVersionPrefix(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/initial.sql": &fstest.MapFile{Data: []byte("-- no prefix")},
	}
	if _, err := parseMigrations(fsys, "migrations"); err == nil {
		t.Fatal("parseMigrations() with a missing numeric prefix should error, got nil")
	}
}

func TestLatestVersion_ReturnsMaxOrZero(t *testing.T) {
	if v := latestVersion(nil); v != 0 {
		t.Fatalf("latestVersion(nil) = %d, want 0", v)
	}
	migrations := []Migration{{Version: 1}, {Version: 3}, {Version: 2}}
	if v := latestVersion(migrations); v != 3 {
		t.Fatalf("latestVersion() = %d, want 3", v)
	}
}
