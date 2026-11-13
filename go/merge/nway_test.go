package merge

import (
	"strings"
	"testing"
)

func TestReconcileTextCleanAndShortCircuits(t *testing.T) {
	tests := []struct {
		name        string
		base        string
		local       string
		remote      string
		wantKind    string
		wantChanged bool
		wantContent string
	}{
		{
			name:        "local equals remote no-op",
			base:        "a\nb\nc",
			local:       "a\nx\nc",
			remote:      "a\nx\nc",
			wantKind:    "clean",
			wantChanged: false,
			wantContent: "a\nx\nc",
		},
		{
			name:        "local unchanged takes remote without pushing",
			base:        "a\nb\nc",
			local:       "a\nb\nc",
			remote:      "a\nb\nd",
			wantKind:    "clean",
			wantChanged: false,
			wantContent: "a\nb\nd",
		},
		{
			name:        "remote unchanged pushes local",
			base:        "a\nb\nc",
			local:       "x\nb\nc",
			remote:      "a\nb\nc",
			wantKind:    "clean",
			wantChanged: true,
			wantContent: "x\nb\nc",
		},
		{
			name:        "disjoint edits merge cleanly",
			base:        "line 1\nline 2\nline 3\nline 4",
			local:       "local 1\nline 2\nline 3\nline 4",
			remote:      "line 1\nline 2\nline 3\nremote 4",
			wantKind:    "clean",
			wantChanged: true,
			wantContent: "local 1\nline 2\nline 3\nremote 4",
		},
		{
			name:        "identical overlapping edit merges cleanly",
			base:        "a\nb\nc",
			local:       "a\nsame\nc",
			remote:      "a\nsame\nc",
			wantKind:    "clean",
			wantChanged: false,
			wantContent: "a\nsame\nc",
		},
		{
			name:        "deletion and disjoint edit merge cleanly",
			base:        "one\ntwo\nthree\nfour",
			local:       "one\nthree\nfour",
			remote:      "one\ntwo\nthree\nfour-updated",
			wantKind:    "clean",
			wantChanged: true,
			wantContent: "one\nthree\nfour-updated",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ReconcileText(tc.base, tc.local, tc.remote)
			if got.Kind != tc.wantKind || got.Changed != tc.wantChanged || got.Content != tc.wantContent {
				t.Fatalf("ReconcileText() = %+v, want kind=%q changed=%v content=%q", got, tc.wantKind, tc.wantChanged, tc.wantContent)
			}
		})
	}
}

func TestReconcileTextMintAndFoldNWay(t *testing.T) {
	base := "header\noriginal line\nfooter"
	local := "header\nlocal edit\nfooter"
	remote := "header\nremote edit\nfooter"

	// 1. Overlapping edit mints backtick-fenced conflict markers (A1=local, A2=remote).
	first := ReconcileText(base, local, remote)
	if first.Kind != "folded" || !first.Changed {
		t.Fatalf("expected folded changed result, got %+v", first)
	}
	wantFirst := strings.Join([]string{
		"header",
		MarkerOpen,
		SegmentMarker("base"),
		"original line",
		SegmentMarker("A1"),
		"local edit",
		SegmentMarker("A2"),
		"remote edit",
		MarkerClose,
		"footer",
	}, "\n")
	if first.Content != wantFirst {
		t.Fatalf("first conflict content mismatch:\ngot:\n%s\nwant:\n%s", first.Content, wantFirst)
	}

	// Round-trip parse/emit check.
	parsed, ok := ParseConflictFile(first.Content)
	if !ok {
		t.Fatal("ParseConflictFile failed on minted output")
	}
	if emitted := EmitConflictFile(parsed); emitted != first.Content {
		t.Fatalf("EmitConflictFile round-trip mismatch:\n%s", emitted)
	}

	// 2. Third device clean edit folds into existing N-way conflict as A3.
	thirdLocal := "header\nthird device edit\nfooter"
	folded := ReconcileText(base, thirdLocal, first.Content)
	if folded.Kind != "folded" || !folded.Changed {
		t.Fatalf("expected folded A3, got %+v", folded)
	}
	if !strings.Contains(folded.Content, SegmentMarker("A3")+"\nthird device edit") {
		t.Fatalf("expected A3 contribution in:\n%s", folded.Content)
	}

	// 3. Re-folding identical edit is idempotent (changed=false).
	idem := ReconcileText(base, thirdLocal, folded.Content)
	if idem.Kind != "folded" || idem.Changed {
		t.Fatalf("expected idempotent no-op fold, got %+v", idem)
	}

	// 4. Editing outside conflict span falls back to keepBoth.
	outOfSpan := "changed header\nthird device edit\nfooter"
	kb := ReconcileText(base, outOfSpan, first.Content)
	if kb.Kind != "keepBoth" {
		t.Fatalf("expected keepBoth for out-of-span edit, got %+v", kb)
	}

	// 5. Malformed conflict markers fall back to keepBoth (never fed to diff3).
	malformed := MarkerOpen + "\n`===== A1`\nbad"
	if res := ReconcileText(base, local, malformed); res.Kind != "keepBoth" {
		t.Fatalf("expected keepBoth for malformed markers, got %+v", res)
	}
}
