package services

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestParseRoster(t *testing.T) {
	tests := []struct {
		name          string
		filename      string
		content       io.Reader
		expectedRows  []ParsedStudentRow
		expectedError string
	}{
		{
			name:     "(a) CSV with all six columns in canonical order",
			filename: "roster.csv",
			content:  bytes.NewBufferString("StudentNumber,StudentIDExt,SecLec,SecLab,FirstName,LastName\n123,EXT123,L1,B1,John,Doe\n456,EXT456,L2,B2,Jane,Smith"),
			expectedRows: []ParsedStudentRow{
				{RowNumber: 2, StudentNumber: "123", StudentIDExt: "EXT123", SecLec: "L1", SecLab: "B1", FirstName: "John", LastName: "Doe"},
				{RowNumber: 3, StudentNumber: "456", StudentIDExt: "EXT456", SecLec: "L2", SecLab: "B2", FirstName: "Jane", LastName: "Smith"},
			},
		},
		{
			name:     "(b) CSV with columns shuffled",
			filename: "roster.csv",
			content:  bytes.NewBufferString("LastName,FirstName,SecLab,SecLec,StudentIDExt,StudentNumber\nDoe,John,B1,L1,EXT123,123"),
			expectedRows: []ParsedStudentRow{
				{RowNumber: 2, StudentNumber: "123", StudentIDExt: "EXT123", SecLec: "L1", SecLab: "B1", FirstName: "John", LastName: "Doe"},
			},
		},
		{
			name:     "(c) CSV using alias headers",
			filename: "roster.csv",
			content:  bytes.NewBufferString("Student No,external_id,lecture_section,lab_section,given name,surname\n123,EXT123,L1,B1,John,Doe"),
			expectedRows: []ParsedStudentRow{
				{RowNumber: 2, StudentNumber: "123", StudentIDExt: "EXT123", SecLec: "L1", SecLab: "B1", FirstName: "John", LastName: "Doe"},
			},
		},
		{
			name:     "(c2) CSV with an optional Email column (alias 'E-mail')",
			filename: "roster.csv",
			content:  bytes.NewBufferString("StudentNumber,StudentIDExt,SecLec,SecLab,FirstName,LastName,E-mail\n123,EXT123,L1,B1,John,Doe,john@example.com"),
			expectedRows: []ParsedStudentRow{
				{RowNumber: 2, StudentNumber: "123", StudentIDExt: "EXT123", SecLec: "L1", SecLab: "B1", FirstName: "John", LastName: "Doe", Email: "john@example.com"},
			},
		},
		{
			name:          "(d) CSV missing the 'Last Name' header",
			filename:      "roster.csv",
			content:       bytes.NewBufferString("StudentNumber,StudentIDExt,SecLec,SecLab,FirstName\n123,EXT123,L1,B1,John"),
			expectedError: "missing required columns: Last Name",
		},
		{
			name:     "(e) CSV with one all-blank row between data rows",
			filename: "roster.csv",
			content:  bytes.NewBufferString("StudentNumber,StudentIDExt,SecLec,SecLab,FirstName,LastName\n123,EXT123,L1,B1,John,Doe\n,,,,,\n456,EXT456,L2,B2,Jane,Smith"),
			expectedRows: []ParsedStudentRow{
				{RowNumber: 2, StudentNumber: "123", StudentIDExt: "EXT123", SecLec: "L1", SecLab: "B1", FirstName: "John", LastName: "Doe"},
				{RowNumber: 4, StudentNumber: "456", StudentIDExt: "EXT456", SecLec: "L2", SecLab: "B2", FirstName: "Jane", LastName: "Smith"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := ParseRoster(tt.filename, tt.content)
			if tt.expectedError != "" {
				if err == nil {
					t.Errorf("ParseRoster() expected error %q, got nil", tt.expectedError)
				} else if err.Error() != tt.expectedError {
					t.Errorf("ParseRoster() expected error %q, got %q", tt.expectedError, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseRoster() unexpected error: %v", err)
			}

			if !reflect.DeepEqual(rows, tt.expectedRows) {
				t.Errorf("ParseRoster() rows = %v, expected %v", rows, tt.expectedRows)
			}
		})
	}

	t.Run("(f) XLSX fixture created in-memory via excelize.NewFile", func(t *testing.T) {
		f := excelize.NewFile()
		defer f.Close()

		sheet := "Sheet1"
		f.SetCellValue(sheet, "A1", "StudentNumber")
		f.SetCellValue(sheet, "B1", "StudentIDExt")
		f.SetCellValue(sheet, "C1", "SecLec")
		f.SetCellValue(sheet, "D1", "SecLab")
		f.SetCellValue(sheet, "E1", "FirstName")
		f.SetCellValue(sheet, "F1", "LastName")

		f.SetCellValue(sheet, "A2", "123")
		f.SetCellValue(sheet, "B2", "EXT123")
		f.SetCellValue(sheet, "C2", "L1")
		f.SetCellValue(sheet, "D2", "B1")
		f.SetCellValue(sheet, "E2", "John")
		f.SetCellValue(sheet, "F2", "Doe")

		var buf bytes.Buffer
		if err := f.Write(&buf); err != nil {
			t.Fatalf("failed to write XLSX to buffer: %v", err)
		}

		expected := []ParsedStudentRow{
			{RowNumber: 2, StudentNumber: "123", StudentIDExt: "EXT123", SecLec: "L1", SecLab: "B1", FirstName: "John", LastName: "Doe"},
		}

		rows, err := ParseRoster("roster.xlsx", &buf)
		if err != nil {
			t.Fatalf("ParseRoster() unexpected error: %v", err)
		}

		if !reflect.DeepEqual(rows, expected) {
			t.Errorf("ParseRoster() rows = %v, expected %v", rows, expected)
		}
	})
}
