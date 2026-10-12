// Package meta reads a video's upload metadata from a Markdown file.
//
// The format is one "## Heading" per field:
//
//	# Anything (ignored)
//
//	## Title
//	One line.
//
//	## Description
//	Any number of lines, blank lines included.
//
//	## Tags
//	comma, separated, tags
//
// Headings are matched case-insensitively and unknown sections are ignored,
// so the file can carry notes of its own.
package meta

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Meta is the metadata found in a file. A field the file does not mention
// is left zero.
type Meta struct {
	Title       string
	Description string
	Tags        []string
}

// ParseFile reads and parses the metadata file at path.
func ParseFile(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, fmt.Errorf("reading metadata file: %w", err)
	}
	return Parse(string(data)), nil
}

// Parse extracts metadata from Markdown text.
func Parse(text string) Meta {
	sections := map[string][]string{}
	current := ""

	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		switch {
		case strings.HasPrefix(line, "## "):
			current = strings.ToLower(strings.TrimSpace(line[3:]))
		case strings.HasPrefix(line, "# "):
			current = ""
		case current != "":
			sections[current] = append(sections[current], line)
		}
	}

	return Meta{
		Title:       strings.Join(strings.Fields(body(sections["title"])), " "),
		Description: body(sections["description"]),
		Tags:        SplitTags(strings.ReplaceAll(body(sections["tags"]), "\n", ",")),
	}
}

// SplitTags turns a comma-separated list into tags, dropping blanks.
func SplitTags(s string) []string {
	var tags []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// body joins a section's lines, trimming the blank lines around it but
// keeping the ones inside.
func body(lines []string) string {
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
