package errcatch

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExcerptLines is how many lines around the error line Excerpt shows.
const ExcerptLines = 5

// Excerpt returns root/rel around line (±n lines), numbered, with the error
// line marked by ">". Empty when the file cannot be read.
func Excerpt(root, rel string, line, n int) string {
	if root == "" || rel == "" || line <= 0 {
		return ""
	}
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return ""
	}
	defer f.Close()
	from, to := max(line-n, 1), line+n
	width := len(fmt.Sprint(to))
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for i := 1; sc.Scan() && i <= to; i++ {
		if i < from {
			continue
		}
		mark := " "
		if i == line {
			mark = ">"
		}
		fmt.Fprintf(&b, "%s %*d | %s\n", mark, width, i, strings.TrimRight(sc.Text(), " \t"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// OriginText is how the report reads in prose: "dev server" / "browser :3001".
func OriginText(r Report) string {
	if r.Source == SourceBrowser {
		if r.Port > 0 {
			return fmt.Sprintf("browser :%d", r.Port)
		}
		return "browser"
	}
	return "dev server"
}

// Instruction is the line the agent gets with the attachment.
func Instruction(r Report) string {
	return fmt.Sprintf("%s で出ているエラーです。原因を調べて直してください。", OriginText(r))
}

// Short reports whether the error fits one line of prompt (no stack, no
// source excerpt): then it is pasted as is instead of written to a file.
func Short(r Report, excerpt string) bool {
	return excerpt == "" && !strings.Contains(strings.TrimSpace(r.Text), "\n") && len(r.Text) <= 200
}

// InlinePrompt is the one-line prompt for a Short report.
func InlinePrompt(r Report) string {
	loc := ""
	if l := r.Loc(); l != "" {
		loc = " (" + l + ")"
	}
	return fmt.Sprintf("%s でエラー: %s%s。原因を調べて直してください。", OriginText(r), strings.TrimSpace(r.Text), loc)
}

// Markdown is the file the agent reads through an @ reference.
func Markdown(n int64, r Report, excerpt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Error #%04d (%s · %s)\n\n", n, OriginText(r), r.Kind)
	fmt.Fprintf(&b, "- message: %s\n", r.Message)
	if r.URL != "" {
		fmt.Fprintf(&b, "- page: %s\n", r.URL)
	}
	if l := r.Loc(); l != "" {
		fmt.Fprintf(&b, "- location: %s\n", l)
	}
	times := "once"
	if r.Count > 1 {
		times = fmt.Sprintf("%d times", r.Count)
	}
	fmt.Fprintf(&b, "- seen: %s (first %s, last %s)\n", times, r.First.Format("15:04:05"), r.Last.Format("15:04:05"))
	fmt.Fprintf(&b, "\n## output\n\n```text\n%s\n```\n", strings.TrimRight(r.Text, "\n"))
	if excerpt != "" {
		fmt.Fprintf(&b, "\n## source: %s\n\n```%s\n%s\n```\n", r.Loc(), fence(r.File), excerpt)
	}
	return b.String()
}

func fence(file string) string {
	switch strings.TrimPrefix(filepath.Ext(file), ".") {
	case "ts", "mts", "cts":
		return "ts"
	case "tsx":
		return "tsx"
	case "js", "mjs", "cjs":
		return "js"
	case "jsx":
		return "jsx"
	case "go":
		return "go"
	case "py":
		return "python"
	case "vue":
		return "vue"
	case "svelte":
		return "svelte"
	}
	return ""
}
