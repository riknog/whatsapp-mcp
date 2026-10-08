package sendqueue

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// TestTypingDelayFormula checks clamp(40 ms x characters, 600, 4000) x jitter
// [0.8, 1.2] over many draws, for lengths below, inside and above the clamp.
func TestTypingDelayFormula(t *testing.T) {
	q := &Queue{rnd: rand.New(rand.NewSource(99))}
	for _, runes := range []int{0, 1, 14, 15, 60, 100, 500} {
		nominal := typingNominal(runes)
		lo := time.Duration(float64(nominal) * 0.8)
		hi := time.Duration(float64(nominal) * 1.2)
		for i := 0; i < 500; i++ {
			d := q.typingDelay(runes)
			if d < lo || d > hi {
				t.Fatalf("runes=%d: delay %v outside [%v, %v]", runes, d, lo, hi)
			}
		}
	}
	if typingNominal(0) != 600*time.Millisecond || typingNominal(1000) != 4*time.Second || typingNominal(20) != 800*time.Millisecond {
		t.Errorf("nominal clamp wrong: %v %v %v", typingNominal(0), typingNominal(1000), typingNominal(20))
	}
}

func TestBuildVCardEscapesAndKeepsOnlyDigits(t *testing.T) {
	v := buildVCard(`A;B,C\D`, "+55 (11) 9999-8888")
	want := `FN:A\;B\,C\\D`
	if !strings.Contains(v, want) || !strings.Contains(v, "waid=551199998888:+551199998888") {
		t.Errorf("vCard = %q", v)
	}
}

func TestCleanNameLimitsAndStripsControls(t *testing.T) {
	long := ""
	for i := 0; i < 150; i++ {
		long += "x"
	}
	if got := cleanName("  a\nb\tc  "); got != "a b c" {
		t.Errorf("cleanName = %q", got)
	}
	if got := cleanName(long); len([]rune(got)) != 100 {
		t.Errorf("len = %d, want 100", len([]rune(got)))
	}
}
