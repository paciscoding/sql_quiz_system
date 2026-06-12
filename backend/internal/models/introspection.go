package models

// ColumnIntrospection holds the schema-level attributes of a single column,
// derived from information_schema and the constraint catalogs. No row data.
type ColumnIntrospection struct {
	Name             string `json:"name"`
	DataType         string `json:"data_type"`
	Nullable         bool   `json:"nullable"`
	IsPrimaryKey     bool   `json:"is_primary_key"`
	IsForeignKey     bool   `json:"is_foreign_key"`
	ReferencesTable  string `json:"references_table,omitempty"`
	ReferencesColumn string `json:"references_column,omitempty"`
}

// TableIntrospection describes one table's schema: name and its ordered columns.
type TableIntrospection struct {
	Name    string                `json:"name"`
	Columns []ColumnIntrospection `json:"columns"`
}
