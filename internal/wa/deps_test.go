package wa

import (
	"os/exec"
	"strings"
	"testing"
)

// TestWhatsmeowOnlyImportedByWAAndIngest enforces CLAUDE.md rule 3 on direct
// imports: only internal/wa and internal/ingest (and the spike build tag, which
// is not part of ./...) may import go.mau.fi/whatsmeow. Transitive dependencies
// are not checked: any package that imports wa depends on whatsmeow by design.
func TestWhatsmeowOnlyImportedByWAAndIngest(t *testing.T) {
	cmd := exec.Command("go", "list", "-f",
		`{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}`, "./...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	allowed := []string{"/internal/wa", "/internal/ingest"}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			t.Fatalf("unexpected go list line: %q", line)
		}
		checked++
		pkg := parts[0]
		if isAllowedPkg(pkg, allowed) {
			continue
		}
		imports := strings.Fields(strings.Join(parts[1:], " "))
		for _, imp := range imports {
			if strings.HasPrefix(imp, "go.mau.fi/whatsmeow") {
				t.Errorf("%s imports %s directly; only internal/wa and internal/ingest may", pkg, imp)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("checked only %d packages; go list output is suspicious", checked)
	}
}

func isAllowedPkg(pkg string, allowed []string) bool {
	for _, a := range allowed {
		if strings.HasSuffix(pkg, a) {
			return true
		}
	}
	return false
}
