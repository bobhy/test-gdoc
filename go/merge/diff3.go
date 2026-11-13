// Package merge implements a pure-Go line-based LCS diff, diff3 merge,
// and Markdown-safe N-way conflict folding engine compatible with vault-share.
package merge

import "slices"

// DiffBlock represents a differing hunk between two line slices (buf1 and buf2)
// as (offset, length) pairs in half-open index space.
type DiffBlock struct {
	Buf1Start int
	Buf1Len   int
	Buf2Start int
	Buf2Len   int
}

// ConflictHunk represents a 3-way conflict span across base (O), local (A), and remote (B).
type ConflictHunk struct {
	O []string
	A []string
	B []string
}

// MergeRegion is either a clean line slice (OK) or a 3-way conflict hunk.
type MergeRegion struct {
	OK       []string
	Conflict *ConflictHunk
}

// DiffIndices computes minimal contiguous differing hunks between buf1 and buf2 using LCS.
func DiffIndices(buf1, buf2 []string) []DiffBlock {
	n := len(buf1)
	m := len(buf2)

	// Trim matching prefix and suffix so LCS runs only on the differing middle.
	prefix := 0
	for prefix < n && prefix < m && buf1[prefix] == buf2[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < (n-prefix) && suffix < (m-prefix) && buf1[n-1-suffix] == buf2[m-1-suffix] {
		suffix++
	}

	sub1 := buf1[prefix : n-suffix]
	sub2 := buf2[prefix : m-suffix]

	matches := lcsPairs(sub1, sub2)
	// Add sentinel pair at the end of the differing middle.
	matches = append(matches, matchPair{i: len(sub1), j: len(sub2)})

	var blocks []DiffBlock
	cur1, cur2 := 0, 0
	for _, p := range matches {
		if cur1 < p.i || cur2 < p.j {
			blocks = append(blocks, DiffBlock{
				Buf1Start: prefix + cur1,
				Buf1Len:   p.i - cur1,
				Buf2Start: prefix + cur2,
				Buf2Len:   p.j - cur2,
			})
		}
		cur1 = p.i + 1
		cur2 = p.j + 1
	}
	return blocks
}

type matchPair struct {
	i int
	j int
}

func lcsPairs(a, b []string) []matchPair {
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return nil
	}
	dp := make([][]int, la+1)
	for i := range dp {
		dp[i] = make([]int, lb+1)
	}
	for i := la - 1; i >= 0; i-- {
		for j := lb - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	pairs := make([]matchPair, 0, dp[0][0])
	i, j := 0, 0
	for i < la && j < lb {
		if a[i] == b[j] {
			pairs = append(pairs, matchPair{i: i, j: j})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return pairs
}

// Diff3Merge performs a 3-way line merge of a (local) and b (remote) against o (base).
func Diff3Merge(a, o, b []string) []MergeRegion {
	hunksA := DiffIndices(o, a)
	hunksB := DiffIndices(o, b)

	var regions []MergeRegion
	basePos := 0
	ia, ib := 0, 0
	deltaA, deltaB := 0, 0

	emitOK := func(lines []string) {
		if len(lines) == 0 {
			return
		}
		copied := slices.Clone(lines)
		if n := len(regions); n > 0 && regions[n-1].Conflict == nil {
			regions[n-1].OK = append(regions[n-1].OK, copied...)
			return
		}
		regions = append(regions, MergeRegion{OK: copied})
	}

	for ia < len(hunksA) || ib < len(hunksB) {
		var nextAStart, nextBStart int
		hasA := ia < len(hunksA)
		hasB := ib < len(hunksB)
		if hasA {
			nextAStart = hunksA[ia].Buf1Start
		}
		if hasB {
			nextBStart = hunksB[ib].Buf1Start
		}

		// Initialize span [spanStart, spanEnd) from the earliest next hunk.
		var spanStart, spanEnd int
		var sideAFirst bool
		if hasA && (!hasB || nextAStart <= nextBStart) {
			spanStart = hunksA[ia].Buf1Start
			spanEnd = spanStart + hunksA[ia].Buf1Len
			sideAFirst = true
		} else {
			spanStart = hunksB[ib].Buf1Start
			spanEnd = spanStart + hunksB[ib].Buf1Len
			sideAFirst = false
		}

		if spanStart > basePos {
			emitOK(o[basePos:spanStart])
		}

		aStart := spanStart + deltaA
		bStart := spanStart + deltaB
		usedA := false
		usedB := false

		if sideAFirst {
			usedA = true
			deltaA += hunksA[ia].Buf2Len - hunksA[ia].Buf1Len
			ia++
		} else {
			usedB = true
			deltaB += hunksB[ib].Buf2Len - hunksB[ib].Buf1Len
			ib++
		}

		// Expand [spanStart, spanEnd) while either side has another overlapping hunk.
		for {
			expanded := false
			for ia < len(hunksA) && hunksOverlap(spanStart, spanEnd, hunksA[ia].Buf1Start, hunksA[ia].Buf1Start+hunksA[ia].Buf1Len) {
				usedA = true
				if end := hunksA[ia].Buf1Start + hunksA[ia].Buf1Len; end > spanEnd {
					spanEnd = end
					expanded = true
				}
				deltaA += hunksA[ia].Buf2Len - hunksA[ia].Buf1Len
				ia++
			}
			for ib < len(hunksB) && hunksOverlap(spanStart, spanEnd, hunksB[ib].Buf1Start, hunksB[ib].Buf1Start+hunksB[ib].Buf1Len) {
				usedB = true
				if end := hunksB[ib].Buf1Start + hunksB[ib].Buf1Len; end > spanEnd {
					spanEnd = end
					expanded = true
				}
				deltaB += hunksB[ib].Buf2Len - hunksB[ib].Buf1Len
				ib++
			}
			if !expanded {
				break
			}
		}

		aEnd := spanEnd + deltaA
		bEnd := spanEnd + deltaB
		oSlice := o[spanStart:spanEnd]
		aSlice := a[aStart:aEnd]
		bSlice := b[bStart:bEnd]

		switch {
		case !usedA && usedB:
			emitOK(bSlice)
		case usedA && !usedB:
			emitOK(aSlice)
		case slices.Equal(aSlice, bSlice):
			emitOK(aSlice)
		default:
			regions = append(regions, MergeRegion{
				Conflict: &ConflictHunk{
					O: slices.Clone(oSlice),
					A: slices.Clone(aSlice),
					B: slices.Clone(bSlice),
				},
			})
		}

		basePos = spanEnd
	}

	if basePos < len(o) {
		emitOK(o[basePos:])
	}
	return regions
}

func hunksOverlap(spanStart, spanEnd, hStart, hEnd int) bool {
	if spanStart == spanEnd && hStart == hEnd {
		return hStart == spanStart
	}
	if spanStart == spanEnd {
		return hStart < spanStart && spanStart < hEnd
	}
	if hStart == hEnd {
		return spanStart < hStart && hStart < spanEnd
	}
	return hStart < spanEnd && spanStart < hEnd
}
