package checker

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// regressionSample mirrors one line of testdata/cases/*.jsonl (design §12.1).
type regressionSample struct {
	ID                  string   `json:"id"`
	Category            string   `json:"category"`
	UserMessage         string   `json:"user_message"`
	Answer              string   `json:"answer"`
	Expected            string   `json:"expected"`
	ExpectedSeverity    string   `json:"expected_severity"`
	ExpectedRuleSignals []string `json:"expected_rule_signals"`
	Notes               string   `json:"notes"`
}

// loadRegressionSamples reads every JSONL file under testdata/cases. It skips
// gracefully while samples-dev has not written them yet.
func loadRegressionSamples(t *testing.T) []regressionSample {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "cases")
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatalf("glob regression samples: %v", err)
	}
	if len(paths) == 0 {
		t.Skipf("regression samples not available yet: no *.jsonl under %s", dir)
	}

	var samples []regressionSample
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open regression samples %s: %v", path, err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
		line := 0
		for scanner.Scan() {
			line++
			raw := strings.TrimSpace(scanner.Text())
			if raw == "" {
				t.Errorf("%s:%d: blank line in JSONL", path, line)
				continue
			}
			var s regressionSample
			if err := json.Unmarshal([]byte(raw), &s); err != nil {
				t.Errorf("%s:%d: invalid JSON sample: %v", path, line, err)
				continue
			}
			if s.ID == "" || s.Category == "" || s.Answer == "" {
				t.Errorf("%s:%d: sample is missing id/category/answer", path, line)
				continue
			}
			samples = append(samples, s)
		}
		if err := scanner.Err(); err != nil {
			t.Errorf("read regression samples %s: %v", path, err)
		}
		_ = f.Close()
	}
	if len(samples) == 0 {
		t.Skipf("regression samples not available yet: no valid samples under %s", dir)
	}
	return samples
}

// hasFindingType reports whether types contains typ.
func hasFindingType(types []FindingType, typ FindingType) bool {
	for _, got := range types {
		if got == typ {
			return true
		}
	}
	return false
}

// coverageSignal reports whether a sample hit the deterministic signal that the
// regression baseline expects for its category.
func coverageSignal(category string, types []FindingType) bool {
	switch category {
	case "duplicate_conclusion":
		return hasFindingType(types, FindingDuplicateParagraph) || hasFindingType(types, FindingDuplicateSentence)
	case "filler_opening":
		return hasFindingType(types, FindingFillerOpening)
	default:
		return false
	}
}

// TestRegressionSamples runs the deterministic rules over the 30-sample
// baseline and enforces the design §12.2 acceptance criteria:
//   - zero material findings on every negative (expected pass) sample,
//   - zero structure_anomaly findings on negative samples,
//   - signal coverage >= 4/5 for duplicate_conclusion and filler_opening.
//
// All per-sample detail is reported through t.Logf so mismatches can be
// reported to the Lead without editing testdata.
func TestRegressionSamples(t *testing.T) {
	samples := loadRegressionSamples(t)

	type categoryStat struct {
		total        int
		withSignal   int
		withMaterial int
	}
	stats := make(map[string]*categoryStat)
	typesByID := make(map[string][]FindingType, len(samples))
	var (
		positives            int
		negatives            int
		negativeMaterial     int
		negativeStructure    int
		expectedSignalMisses []string
	)
	coverageCategories := []string{"duplicate_conclusion", "filler_opening"}

	for _, sample := range samples {
		rep := Analyze(sample.Answer)
		types := rep.Types()
		typesByID[sample.ID] = types

		stat := stats[sample.Category]
		if stat == nil {
			stat = &categoryStat{}
			stats[sample.Category] = stat
		}
		stat.total++
		if len(types) > 0 {
			stat.withSignal++
		}
		if rep.HasMaterial() {
			stat.withMaterial++
		}

		t.Logf("%-22s %-20s natural=%-5d findings=%v", sample.ID, sample.Category, rep.Stats.NaturalRunes, types)

		switch sample.Category {
		case "good_long", "good_short":
			negatives++
			if rep.HasMaterial() {
				negativeMaterial++
				t.Errorf("%s (%s): negative sample has material findings: %#v", sample.ID, sample.Category, rep.Findings)
			}
			if findings := findingsOfType(rep, FindingStructureAnomaly); len(findings) > 0 {
				negativeStructure++
				t.Errorf("%s (%s): structure_anomaly must not fire on negative samples: %#v", sample.ID, sample.Category, findings)
			}
		default:
			positives++
			for _, expected := range sample.ExpectedRuleSignals {
				if !hasFindingType(types, FindingType(expected)) {
					expectedSignalMisses = append(expectedSignalMisses, sample.ID+"→"+expected)
					break
				}
			}
		}
	}

	t.Logf("regression summary: samples=%d positives=%d negatives=%d negative_material=%d negative_structure=%d",
		len(samples), positives, negatives, negativeMaterial, negativeStructure)

	categories := make([]string, 0, len(stats))
	for category := range stats {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	for _, category := range categories {
		stat := stats[category]
		t.Logf("coverage %-22s %d/%d samples with findings, %d with material findings",
			category, stat.withSignal, stat.total, stat.withMaterial)
	}
	if len(expectedSignalMisses) > 0 {
		t.Logf("expected_rule_signals misses (report only): %v", expectedSignalMisses)
	}

	if negatives == 0 {
		t.Fatalf("no negative samples loaded: cannot verify false-positive rate")
	}
	if negativeMaterial != 0 {
		t.Errorf("false-positive rate: %d/%d negative samples produced material findings, want 0/%d",
			negativeMaterial, negatives, negatives)
	}

	for _, category := range coverageCategories {
		stat := stats[category]
		if stat == nil || stat.total == 0 {
			t.Errorf("coverage category %q missing from regression baseline", category)
			continue
		}
		covered := 0
		for _, sample := range samples {
			if sample.Category == category && coverageSignal(category, typesByID[sample.ID]) {
				covered++
			}
		}
		if covered*5 < stat.total*4 {
			t.Errorf("signal coverage for %q = %d/%d, want >= 4/5", category, covered, stat.total)
		}
	}
}
