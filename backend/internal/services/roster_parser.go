package services

import (
	"encoding/csv"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ParsedStudentRow holds the parsed data for one student from the roster file.
// RowNumber is 1-based (header = row 1, first data row = row 2).
type ParsedStudentRow struct {
	RowNumber     int
	StudentNumber string
	StudentIDExt  string
	SecLec        string
	SecLab        string
	FirstName     string
	LastName      string
	Email         string
}

var headerAliases = map[string][]string{
	"StudentNumber": {"student number", "student_number", "studentnumber", "student no"},
	"StudentIDExt":  {"student_id", "student id", "student_id_ext", "external_id"},
	"SecLec":        {"seclec", "sec_lec", "lecture_section"},
	"SecLab":        {"seclab", "sec_lab", "lab_section"},
	"FirstName":     {"first name", "first_name", "firstname", "given name"},
	"LastName":      {"last name", "last_name", "lastname", "surname", "family name"},
	// Email is OPTIONAL: absent → empty. Alias-tolerant.
	"Email": {"email", "e-mail", "email address", "e_mail", "mail"},
}

// ParseRoster parses a CSV or XLSX roster file into a slice of ParsedStudentRow.
// The format is detected from the filename extension (.xlsx or .csv).
// Header matching is case-insensitive; see the alias table in ARCH-003.
// Returns (nil, err) when required headers are missing.
// Rows where all six fields are empty after trimming are silently skipped.
func ParseRoster(filename string, r io.Reader) ([]ParsedStudentRow, error) {
	ext := strings.ToLower(filepath.Ext(filename))

	var rows [][]string
	var err error

	switch ext {
	case ".csv":
		reader := csv.NewReader(r)
		rows, err = reader.ReadAll()
		if err != nil {
			return nil, fmt.Errorf("failed to read CSV: %w", err)
		}
	case ".xlsx":
		f, err := excelize.OpenReader(r)
		if err != nil {
			return nil, fmt.Errorf("failed to open XLSX: %w", err)
		}
		defer f.Close()

		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return nil, fmt.Errorf("XLSX file has no sheets")
		}
		rows, err = f.GetRows(sheets[0])
		if err != nil {
			return nil, fmt.Errorf("failed to get rows from XLSX: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported file format: %s", ext)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("file is empty")
	}

	headerRow := rows[0]
	indices := make(map[string]int)

	for i, header := range headerRow {
		cleanHeader := strings.ToLower(strings.TrimSpace(header))
		for canonical, aliases := range headerAliases {
			if cleanHeader == strings.ToLower(canonical) {
				indices[canonical] = i
				break
			}
			for _, alias := range aliases {
				if cleanHeader == alias {
					indices[canonical] = i
					break
				}
			}
		}
	}

	var missingHeaders []string
	requiredHeaders := []string{"StudentNumber", "StudentIDExt", "SecLec", "SecLab", "FirstName", "LastName"}
	displayNames := map[string]string{
		"StudentNumber": "Student Number",
		"StudentIDExt":  "Student ID",
		"SecLec":        "SecLec",
		"SecLab":        "SecLab",
		"FirstName":     "First Name",
		"LastName":      "Last Name",
	}
	for _, h := range requiredHeaders {
		if _, ok := indices[h]; !ok {
			missingHeaders = append(missingHeaders, displayNames[h])
		}
	}

	if len(missingHeaders) > 0 {
		return nil, fmt.Errorf("missing required columns: %s", strings.Join(missingHeaders, ", "))
	}

	var results []ParsedStudentRow
	for i := 1; i < len(rows); i++ {
		row := rows[i]
		parsed := ParsedStudentRow{
			RowNumber: i + 1,
		}

		// Ensure we don't index out of bounds if a row is shorter than the header,
		// and return "" for optional columns (e.g. Email) that are simply absent —
		// without a presence check, a missing column's zero-value index (0) would
		// wrongly alias the first column.
		getValue := func(canonical string) string {
			idx, ok := indices[canonical]
			if !ok {
				return ""
			}
			if idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}

		parsed.StudentNumber = getValue("StudentNumber")
		parsed.StudentIDExt = getValue("StudentIDExt")
		parsed.SecLec = getValue("SecLec")
		parsed.SecLab = getValue("SecLab")
		parsed.FirstName = getValue("FirstName")
		parsed.LastName = getValue("LastName")
		// Email is optional; getValue returns "" when the column is absent.
		parsed.Email = getValue("Email")

		if parsed.StudentNumber == "" && parsed.StudentIDExt == "" && parsed.SecLec == "" &&
			parsed.SecLab == "" && parsed.FirstName == "" && parsed.LastName == "" {
			continue
		}

		results = append(results, parsed)
	}

	return results, nil
}
