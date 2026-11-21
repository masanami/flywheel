package core

import "testing"

// 成果物の種類の閉集合は、記録できる種類（artifactKind.valid）と一致する。
func TestArtifactKindValues(t *testing.T) {
	got := ArtifactKindValues()
	want := []string{"pr", "branch", "commit"}
	if len(got) != len(want) {
		t.Fatalf("ArtifactKindValues() = %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("ArtifactKindValues()[%d] = %q, want %q", i, got[i], v)
		}
		if !artifactKind(v).valid() {
			t.Errorf("%q は valid でない", v)
		}
	}
	if artifactKind("zzz").valid() {
		t.Error("zzz は valid であってはならない")
	}
}
