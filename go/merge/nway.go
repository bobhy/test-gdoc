package merge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// MarkerOpen begins an N-way conflict region using Markdown-safe backticks.
	MarkerOpen = "`<<<<< conflict`"
	// MarkerClose ends an N-way conflict region.
	MarkerClose = "`>>>>> conflict`"
	// BaseLabel identifies the common-ancestor segment inside a conflict region.
	BaseLabel = "base"
)

var (
	segmentMarkerRe = regexp.MustCompile("^`===== (base|A\\d+)`$")
	markerLikeRe    = regexp.MustCompile("^`(<<<<<|>>>>>|=====|\\|\\|\\|\\|\\|)")
)

// SegmentMarker returns the formatted introducer line for a segment label (e.g., "`===== A1`").
func SegmentMarker(label string) string {
	return "`===== " + label + "`"
}

// RegionSegment holds the lines belonging to base or one alternative (A1, A2, ...).
type RegionSegment struct {
	Label string
	Lines []string
}

// ConflictRegion holds the base segment and N >= 2 distinct alternatives for one conflicted span.
type ConflictRegion struct {
	Segments []RegionSegment
}

// Segment represents either a stable line run or a conflict region.
type Segment struct {
	Kind   string // "stable" or "region"
	Lines  []string
	Region *ConflictRegion
}

// ParsedFile represents a file decomposed into stable runs interleaved with conflict regions.
type ParsedFile struct {
	Segments []Segment
}

// ReconcileResult reports the outcome of ReconcileText.
// Kind is "clean", "folded", or "keepBoth".
// Changed is true when the Drive copy differs from the reconciled content.
type ReconcileResult struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
	Changed bool   `json:"changed"`
}

// HasConflictMarkers reports whether text contains the conflict-open delimiter.
func HasConflictMarkers(text string) bool {
	return strings.Contains(text, MarkerOpen)
}

func splitLines(text string) []string {
	return strings.Split(text, "\n")
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func isMarkerLike(line string) bool {
	return markerLikeRe.MatchString(line)
}

func looksLikeConflict(text string) bool {
	return slices.ContainsFunc(splitLines(text), isMarkerLike)
}

func validRegion(segs []RegionSegment) bool {
	baseCount := 0
	for _, s := range segs {
		if s.Label == BaseLabel {
			baseCount++
		}
	}
	altCount := len(segs) - baseCount
	return baseCount == 1 && altCount >= 2
}

func parseRegion(lines []string, start int) (*ConflictRegion, int, bool) {
	var segs []RegionSegment
	var current *RegionSegment

	for i := start; i < len(lines); i++ {
		line := lines[i]
		if line == MarkerClose {
			if current != nil {
				segs = append(segs, *current)
			}
			if !validRegion(segs) {
				return nil, 0, false
			}
			return &ConflictRegion{Segments: segs}, i + 1, true
		}
		if line == MarkerOpen {
			return nil, 0, false
		}
		if m := segmentMarkerRe.FindStringSubmatch(line); m != nil {
			if current != nil {
				segs = append(segs, *current)
			}
			current = &RegionSegment{Label: m[1], Lines: make([]string, 0)}
			continue
		}
		if isMarkerLike(line) || current == nil {
			return nil, 0, false
		}
		current.Lines = append(current.Lines, line)
	}
	return nil, 0, false
}

// ParseConflictFile decomposes text into stable runs and validated conflict regions.
// Returns (nil, false) when any conflict markers are unbalanced or malformed.
func ParseConflictFile(text string) (*ParsedFile, bool) {
	lines := splitLines(text)
	var segments []Segment
	var stable []string
	flush := func() {
		if len(stable) > 0 {
			segments = append(segments, Segment{Kind: "stable", Lines: stable})
			stable = nil
		}
	}

	for i := 0; i < len(lines); {
		line := lines[i]
		if line == MarkerOpen {
			flush()
			region, nextIdx, ok := parseRegion(lines, i+1)
			if !ok {
				return nil, false
			}
			segments = append(segments, Segment{Kind: "region", Region: region})
			i = nextIdx
			continue
		}
		if isMarkerLike(line) {
			return nil, false
		}
		stable = append(stable, line)
		i++
	}
	flush()
	return &ParsedFile{Segments: segments}, true
}

// EmitConflictFile serializes a ParsedFile back to Markdown-safe text.
func EmitConflictFile(parsed *ParsedFile) string {
	var out []string
	for _, seg := range parsed.Segments {
		if seg.Kind == "stable" {
			out = append(out, seg.Lines...)
			continue
		}
		out = append(out, MarkerOpen)
		for _, s := range seg.Region.Segments {
			out = append(out, SegmentMarker(s.Label))
			out = append(out, s.Lines...)
		}
		out = append(out, MarkerClose)
	}
	return joinLines(out)
}

func regionsOf(p *ParsedFile) []*ConflictRegion {
	var out []*ConflictRegion
	for _, seg := range p.Segments {
		if seg.Kind == "region" {
			out = append(out, seg.Region)
		}
	}
	return out
}

func baseSegmentOf(r *ConflictRegion) []string {
	for _, s := range r.Segments {
		if s.Label == BaseLabel {
			return s.Lines
		}
	}
	return nil
}

func baseReconLines(p *ParsedFile) []string {
	var out []string
	for _, seg := range p.Segments {
		if seg.Kind == "stable" {
			out = append(out, seg.Lines...)
		} else {
			out = append(out, baseSegmentOf(seg.Region)...)
		}
	}
	return out
}

type baseRange struct {
	start int
	end   int
}

func regionRanges(p *ParsedFile) []baseRange {
	var ranges []baseRange
	idx := 0
	for _, seg := range p.Segments {
		if seg.Kind == "stable" {
			idx += len(seg.Lines)
			continue
		}
		length := len(baseSegmentOf(seg.Region))
		ranges = append(ranges, baseRange{start: idx, end: idx + length})
		idx += length
	}
	return ranges
}

func skeletonOf(p *ParsedFile) string {
	type skelItem struct {
		Kind  string   `json:"k"`
		Lines []string `json:"l"`
	}
	items := make([]skelItem, 0, len(p.Segments))
	for _, s := range p.Segments {
		if s.Kind == "stable" {
			items = append(items, skelItem{Kind: "S", Lines: s.Lines})
		} else {
			items = append(items, skelItem{Kind: "R", Lines: baseSegmentOf(s.Region)})
		}
	}
	b, _ := json.Marshal(items)
	return string(b)
}

type contribVec = [][]string // nil element indicates contribution matches base in that region

func normKey(vec contribVec, regions []*ConflictRegion) string {
	resolved := make([][]string, len(vec))
	for i, s := range vec {
		if s == nil {
			resolved[i] = baseSegmentOf(regions[i])
		} else {
			resolved[i] = s
		}
		if resolved[i] == nil {
			resolved[i] = []string{}
		}
	}
	b, _ := json.Marshal(resolved)
	return string(b)
}

func contributionsOf(p *ParsedFile) ([]string, map[string]contribVec) {
	regions := regionsOf(p)
	m := make(map[string]contribVec)
	var order []string
	for ri, reg := range regions {
		for _, seg := range reg.Segments {
			if seg.Label == BaseLabel {
				continue
			}
			vec, ok := m[seg.Label]
			if !ok {
				vec = make(contribVec, len(regions))
				m[seg.Label] = vec
				order = append(order, seg.Label)
			}
			vec[ri] = seg.Lines
		}
	}
	return order, m
}

func maxAltNumber(labels []string) int {
	maxVal := 0
	for _, l := range labels {
		if strings.HasPrefix(l, "A") {
			if n, err := strconv.Atoi(l[1:]); err == nil && n > maxVal {
				maxVal = n
			}
		}
	}
	return maxVal
}

type orderedContrib struct {
	label string
	vec   contribVec
}

func emitFromContributions(skeleton *ParsedFile, regions []*ConflictRegion, contribs []orderedContrib) string {
	segments := make([]Segment, 0, len(skeleton.Segments))
	ri := 0
	for _, seg := range skeleton.Segments {
		if seg.Kind == "stable" {
			segments = append(segments, seg)
			continue
		}
		region := regions[ri]
		segs := []RegionSegment{{Label: BaseLabel, Lines: baseSegmentOf(region)}}
		for _, c := range contribs {
			if lines := c.vec[ri]; lines != nil {
				segs = append(segs, RegionSegment{Label: c.label, Lines: lines})
			}
		}
		segments = append(segments, Segment{Kind: "region", Region: &ConflictRegion{Segments: segs}})
		ri++
	}
	return EmitConflictFile(&ParsedFile{Segments: segments})
}

func localizeClean(anchor *ParsedFile, base, cleanText string) (contribVec, bool) {
	recon := baseReconLines(anchor)
	if joinLines(recon) != base {
		return nil, false
	}
	cleanLines := splitLines(cleanText)
	ranges := regionRanges(anchor)
	blocks := DiffIndices(recon, cleanLines)

	for _, b := range blocks {
		inside := false
		for _, r := range ranges {
			if b.Buf1Start >= r.start && b.Buf1Start+b.Buf1Len <= r.end {
				inside = true
				break
			}
		}
		if !inside {
			return nil, false
		}
	}

	regions := regionsOf(anchor)
	slicesVec := make(contribVec, len(ranges))
	shift := 0
	bi := 0
	for ri, r := range ranges {
		for bi < len(blocks) && blocks[bi].Buf1Start+blocks[bi].Buf1Len <= r.start {
			shift += blocks[bi].Buf2Len - blocks[bi].Buf1Len
			bi++
		}
		cStart := r.start + shift
		withinDelta := 0
		for bi < len(blocks) && blocks[bi].Buf1Start >= r.start && blocks[bi].Buf1Start+blocks[bi].Buf1Len <= r.end {
			withinDelta += blocks[bi].Buf2Len - blocks[bi].Buf1Len
			bi++
		}
		shift += withinDelta
		slice := cleanLines[cStart : r.end+shift]
		if slices.Equal(slice, baseSegmentOf(regions[ri])) {
			slicesVec[ri] = nil
		} else {
			slicesVec[ri] = slice
		}
	}
	return slicesVec, true
}

func mint(base, local, remote string) ReconcileResult {
	regions := Diff3Merge(splitLines(local), splitLines(base), splitLines(remote))
	var segments []Segment
	var stable []string
	conflicted := false
	flush := func() {
		if len(stable) > 0 {
			segments = append(segments, Segment{Kind: "stable", Lines: stable})
			stable = nil
		}
	}

	for _, r := range regions {
		if r.Conflict == nil {
			stable = append(stable, r.OK...)
			continue
		}
		conflicted = true
		flush()
		segments = append(segments, Segment{
			Kind: "region",
			Region: &ConflictRegion{
				Segments: []RegionSegment{
					{Label: BaseLabel, Lines: nonNilSlice(r.Conflict.O)},
					{Label: "A1", Lines: nonNilSlice(r.Conflict.A)},
					{Label: "A2", Lines: nonNilSlice(r.Conflict.B)},
				},
			},
		})
	}
	flush()

	content := EmitConflictFile(&ParsedFile{Segments: segments})
	if conflicted {
		return ReconcileResult{Kind: "folded", Changed: content != remote, Content: content}
	}
	return ReconcileResult{Kind: "clean", Changed: content != remote, Content: content}
}

func nonNilSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func foldInto(anchor *ParsedFile, anchorText, base string, incoming []contribVec) ReconcileResult {
	if joinLines(baseReconLines(anchor)) != base {
		return ReconcileResult{Kind: "keepBoth"}
	}
	regions := regionsOf(anchor)
	order, cmap := contributionsOf(anchor)

	seen := make(map[string]struct{}, len(cmap))
	for _, vec := range cmap {
		seen[normKey(vec, regions)] = struct{}{}
	}
	baseKey := normKey(make(contribVec, len(regions)), regions)

	var fresh []contribVec
	for _, vec := range incoming {
		key := normKey(vec, regions)
		if key == baseKey {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		fresh = append(fresh, vec)
	}
	if len(fresh) == 0 {
		return ReconcileResult{Kind: "folded", Changed: false, Content: anchorText}
	}

	n := maxAltNumber(order)
	contribs := make([]orderedContrib, 0, len(order)+len(fresh))
	for _, label := range order {
		contribs = append(contribs, orderedContrib{label: label, vec: cmap[label]})
	}
	for _, vec := range fresh {
		n++
		contribs = append(contribs, orderedContrib{label: fmt.Sprintf("A%d", n), vec: vec})
	}
	return ReconcileResult{
		Kind:    "folded",
		Changed: true,
		Content: emitFromContributions(anchor, regions, contribs),
	}
}

func asResult(content string, push bool) ReconcileResult {
	if !looksLikeConflict(content) {
		return ReconcileResult{Kind: "clean", Changed: push, Content: content}
	}
	if _, ok := ParseConflictFile(content); !ok {
		return ReconcileResult{Kind: "keepBoth"}
	}
	return ReconcileResult{Kind: "folded", Changed: push, Content: content}
}

// ReconcileText reconciles local and remote text against their common base.
// Clean inputs merge via Diff3Merge; existing N-way conflict files fold distinct
// contributions without ever feeding conflict markers back into diff3.
func ReconcileText(base, local, remote string) ReconcileResult {
	if local == remote {
		return asResult(local, false)
	}
	if local == base {
		return asResult(remote, false)
	}
	if remote == base {
		return asResult(local, true)
	}

	cleanBase := base
	if looksLikeConflict(base) {
		bp, ok := ParseConflictFile(base)
		if !ok {
			return ReconcileResult{Kind: "keepBoth"}
		}
		cleanBase = joinLines(baseReconLines(bp))
	}

	localMarked := looksLikeConflict(local)
	remoteMarked := looksLikeConflict(remote)

	if !localMarked && !remoteMarked {
		return mint(cleanBase, local, remote)
	}

	var localParsed, remoteParsed *ParsedFile
	if localMarked {
		var ok bool
		localParsed, ok = ParseConflictFile(local)
		if !ok {
			return ReconcileResult{Kind: "keepBoth"}
		}
	}
	if remoteMarked {
		var ok bool
		remoteParsed, ok = ParseConflictFile(remote)
		if !ok {
			return ReconcileResult{Kind: "keepBoth"}
		}
	}

	// Clean local folded into N-way remote (Drive is anchor).
	if !localMarked && remoteParsed != nil {
		vec, ok := localizeClean(remoteParsed, cleanBase, local)
		if !ok {
			return ReconcileResult{Kind: "keepBoth"}
		}
		return foldInto(remoteParsed, remote, cleanBase, []contribVec{vec})
	}

	// N-way local, clean remote (local is anchor; Drive needs our richer conflict file).
	if localParsed != nil && !remoteMarked {
		vec, ok := localizeClean(localParsed, cleanBase, remote)
		if !ok {
			return ReconcileResult{Kind: "keepBoth"}
		}
		r := foldInto(localParsed, local, cleanBase, []contribVec{vec})
		if r.Kind == "folded" {
			r.Changed = true
		}
		return r
	}

	// Both N-way: Drive is anchor; fold local's contributions in.
	if localParsed != nil && remoteParsed != nil {
		if skeletonOf(localParsed) != skeletonOf(remoteParsed) {
			return ReconcileResult{Kind: "keepBoth"}
		}
		lOrder, lMap := contributionsOf(localParsed)
		incoming := make([]contribVec, 0, len(lOrder))
		for _, label := range lOrder {
			incoming = append(incoming, lMap[label])
		}
		return foldInto(remoteParsed, remote, cleanBase, incoming)
	}

	return ReconcileResult{Kind: "keepBoth"}
}
