package openapi

import (
	"strings"

	"github.com/example/openapi-mocker/internal/usecase"
)

// maxDiffCells limits the size of the LCS table (O(n*m) in memory/time),
// so an abnormally large contract doesn't crash the process.
const maxDiffCells = 4_000_000

func diffLines(a, b string) []usecase.DiffLine {
	al := splitLines(a)
	bl := splitLines(b)
	n, m := len(al), len(bl)

	if n*m > maxDiffCells {
		return coarseDiff(al, bl)
	}

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case al[i] == bl[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	out := make([]usecase.DiffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			out = append(out, usecase.DiffLine{Type: usecase.DiffSame, Text: al[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, usecase.DiffLine{Type: usecase.DiffRemoved, Text: al[i]})
			i++
		default:
			out = append(out, usecase.DiffLine{Type: usecase.DiffAdded, Text: bl[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, usecase.DiffLine{Type: usecase.DiffRemoved, Text: al[i]})
	}
	for ; j < m; j++ {
		out = append(out, usecase.DiffLine{Type: usecase.DiffAdded, Text: bl[j]})
	}
	return out
}

func coarseDiff(al, bl []string) []usecase.DiffLine {
	out := make([]usecase.DiffLine, 0, len(al)+len(bl))
	for _, l := range al {
		out = append(out, usecase.DiffLine{Type: usecase.DiffRemoved, Text: l})
	}
	for _, l := range bl {
		out = append(out, usecase.DiffLine{Type: usecase.DiffAdded, Text: l})
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}
