package factory

import (
	"fmt"
	"strings"
)

const diffContextLines = 3

type diffOpKind byte

const (
	diffEqual  diffOpKind = ' '
	diffDelete diffOpKind = '-'
	diffInsert diffOpKind = '+'
)

type diffOp struct {
	kind diffOpKind
	text string
}

// UnifiedDiff formats a unified diff from oldText to newText with three lines
// of context around each change. It returns "" when the texts are identical.
func UnifiedDiff(oldLabel, newLabel, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	oldLines, oldNoNL := splitDiffLines(oldText)
	newLines, newNoNL := splitDiffLines(newText)
	ops := diffLines(oldLines, newLines)

	type hunk struct {
		oldStart, oldCount int
		newStart, newCount int
		lines              []string
	}

	// Find indices of changed operations so we can group them into hunks separated
	// by more than 2*diffContextLines equal lines.
	var hunks []hunk
	oldLine, newLine := 1, 1
	// Track each op's 1-based line numbers before the op runs.
	oldPos := make([]int, len(ops)+1)
	newPos := make([]int, len(ops)+1)
	for i, op := range ops {
		oldPos[i] = oldLine
		newPos[i] = newLine
		switch op.kind {
		case diffEqual:
			oldLine++
			newLine++
		case diffDelete:
			oldLine++
		case diffInsert:
			newLine++
		}
	}
	oldPos[len(ops)] = oldLine
	newPos[len(ops)] = newLine

	i := 0
	for i < len(ops) {
		if ops[i].kind == diffEqual {
			i++
			continue
		}
		start := max(0, i-diffContextLines)
		end := i
		for end < len(ops) {
			if ops[end].kind != diffEqual {
				end++
				continue
			}
			// Count consecutive equal lines.
			eqEnd := end
			for eqEnd < len(ops) && ops[eqEnd].kind == diffEqual {
				eqEnd++
			}
			if eqEnd < len(ops) && eqEnd-end <= 2*diffContextLines {
				end = eqEnd
				continue
			}
			end = min(len(ops), end+diffContextLines)
			break
		}

		h := hunk{
			oldStart: oldPos[start],
			newStart: newPos[start],
		}
		for j := start; j < end; j++ {
			op := ops[j]
			h.lines = append(h.lines, string(op.kind)+op.text)
			switch op.kind {
			case diffEqual:
				h.oldCount++
				h.newCount++
				if oldNoNL && oldPos[j] == len(oldLines) && newNoNL && newPos[j] == len(newLines) {
					h.lines = append(h.lines, `\ No newline at end of file`)
				}
			case diffDelete:
				h.oldCount++
				if oldNoNL && oldPos[j] == len(oldLines) {
					h.lines = append(h.lines, `\ No newline at end of file`)
				}
			case diffInsert:
				h.newCount++
				if newNoNL && newPos[j] == len(newLines) {
					h.lines = append(h.lines, `\ No newline at end of file`)
				}
			}
		}
		if h.oldCount == 0 {
			h.oldStart--
		}
		if h.newCount == 0 {
			h.newStart--
		}
		hunks = append(hunks, h)
		i = end
	}

	// If only the trailing newline differed, emit a single hunk on the last line.
	if len(hunks) == 0 && oldNoNL != newNoNL && len(oldLines) > 0 {
		last := len(oldLines) - 1
		start := max(0, last-diffContextLines)
		h := hunk{
			oldStart: start + 1,
			oldCount: len(oldLines) - start,
			newStart: start + 1,
			newCount: len(newLines) - start,
		}
		for j := start; j < last; j++ {
			h.lines = append(h.lines, " "+oldLines[j])
		}
		h.lines = append(h.lines, "-"+oldLines[last])
		if oldNoNL {
			h.lines = append(h.lines, `\ No newline at end of file`)
		}
		h.lines = append(h.lines, "+"+newLines[last])
		if newNoNL {
			h.lines = append(h.lines, `\ No newline at end of file`)
		}
		hunks = append(hunks, h)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldLabel, newLabel)
	for _, h := range hunks {
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", formatRange(h.oldStart, h.oldCount), formatRange(h.newStart, h.newCount))
		for _, line := range h.lines {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func formatRange(start, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func splitDiffLines(text string) ([]string, bool) {
	if text == "" {
		return nil, false
	}
	noTrailingNL := !strings.HasSuffix(text, "\n")
	trimmed := strings.TrimSuffix(text, "\n")
	return strings.Split(trimmed, "\n"), noTrailingNL
}

// diffLines computes a minimal edit script between a and b using Myers' O((N+M)D) algorithm.
func diffLines(a, b []string) []diffOp {
	// Trim common prefix and suffix to keep the search small.
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	midA := a[prefix : len(a)-suffix]
	midB := b[prefix : len(b)-suffix]

	var ops []diffOp
	for _, line := range a[:prefix] {
		ops = append(ops, diffOp{kind: diffEqual, text: line})
	}
	ops = append(ops, myersDiff(midA, midB)...)
	for _, line := range a[len(a)-suffix:] {
		ops = append(ops, diffOp{kind: diffEqual, text: line})
	}
	return ops
}

func myersDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 {
		ops := make([]diffOp, m)
		for i, line := range b {
			ops[i] = diffOp{kind: diffInsert, text: line}
		}
		return ops
	}
	if m == 0 {
		ops := make([]diffOp, n)
		for i, line := range a {
			ops[i] = diffOp{kind: diffDelete, text: line}
		}
		return ops
	}

	maxD := n + m
	offset := maxD
	v := make([]int, 2*maxD+1)
	trace := make([][]int, 0, min(maxD+1, 64))

	foundD := maxD
search:
	for d := 0; d <= maxD; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				foundD = d
				break search
			}
		}
	}

	var reversed []diffOp
	x, y := n, m
	for d := foundD; d > 0; d-- {
		prev := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && prev[offset+k-1] < prev[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := prev[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			reversed = append(reversed, diffOp{kind: diffEqual, text: a[x]})
		}
		if x == prevX {
			y--
			reversed = append(reversed, diffOp{kind: diffInsert, text: b[y]})
		} else {
			x--
			reversed = append(reversed, diffOp{kind: diffDelete, text: a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		reversed = append(reversed, diffOp{kind: diffEqual, text: a[x]})
	}

	for l, r := 0, len(reversed)-1; l < r; l, r = l+1, r-1 {
		reversed[l], reversed[r] = reversed[r], reversed[l]
	}
	return reversed
}
