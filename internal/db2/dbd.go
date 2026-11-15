// Package db2 reads WDC3/WDC4/WDC5 client database tables using WoWDBDefs
// (https://github.com/wowdev/WoWDBDefs) definitions to name record fields.
package db2

import (
	"fmt"
	"strconv"
	"strings"
)

// Field is one entry of a DBD layout definition, in record order.
type Field struct {
	Name     string
	Bits     int // element size in bits (8/16/32/64)
	Signed   bool
	Float    bool
	Count    int  // array length, 1 for scalars
	Inline   bool // false for $noninline$ fields (ID list / relationship data)
	ID       bool
	Relation bool
}

// ParseDBD returns the fields of the definition block whose LAYOUT line lists
// layoutHash (eight uppercase hex digits, as stored in the DB2 header).
func ParseDBD(text, layoutHash string) ([]Field, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	columns := map[string]string{}
	for _, block := range strings.Split(text, "\n\n") {
		var lines []string
		for _, line := range strings.Split(block, "\n") {
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i]
			}
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			continue
		}
		if lines[0] == "COLUMNS" {
			for _, line := range lines[1:] {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					typ, _, _ := strings.Cut(parts[0], "<")
					columns[strings.TrimSuffix(parts[1], "?")] = typ
				}
			}
			continue
		}
		matched := false
		var fields []Field
		for _, line := range lines {
			switch {
			case strings.HasPrefix(line, "LAYOUT "):
				for _, hash := range strings.Split(line[7:], ",") {
					matched = matched || strings.EqualFold(strings.TrimSpace(hash), layoutHash)
				}
			case strings.HasPrefix(line, "BUILD "), strings.HasPrefix(line, "COMMENT "):
			default:
				f, err := parseFieldLine(line, columns)
				if err != nil {
					return nil, err
				}
				fields = append(fields, f)
			}
		}
		if matched {
			return fields, nil
		}
	}
	return nil, fmt.Errorf("no definition for layout %s", layoutHash)
}

func parseFieldLine(line string, columns map[string]string) (Field, error) {
	f := Field{Inline: true, Count: 1, Bits: 32, Signed: true}
	if strings.HasPrefix(line, "$") {
		end := strings.IndexByte(line[1:], '$')
		if end < 0 {
			return f, fmt.Errorf("bad annotation")
		}
		for _, a := range strings.Split(line[1:end+1], ",") {
			switch a {
			case "noninline":
				f.Inline = false
			case "id":
				f.ID = true
			case "relation":
				f.Relation = true
			}
		}
		line = line[end+2:]
	}
	if i := strings.IndexByte(line, '['); i >= 0 {
		n, err := strconv.Atoi(strings.TrimSuffix(line[i+1:], "]"))
		if err != nil || n < 1 {
			return f, fmt.Errorf("bad array %q", line)
		}
		f.Count, line = n, line[:i]
	}
	if i := strings.IndexByte(line, '<'); i >= 0 {
		size := strings.TrimSuffix(line[i+1:], ">")
		if strings.HasPrefix(size, "u") {
			f.Signed, size = false, size[1:]
		}
		n, err := strconv.Atoi(size)
		if err != nil {
			return f, fmt.Errorf("bad size %q", line)
		}
		f.Bits, line = n, line[:i]
	}
	f.Name = line
	switch columns[f.Name] {
	case "float":
		f.Float, f.Signed = true, false
	case "string", "locstring":
		f.Signed = false
	}
	return f, nil
}
