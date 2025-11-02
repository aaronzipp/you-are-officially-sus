package models

// Location represents a place/word with categories
type Location struct {
	Word       string   `json:"word"`
	Categories []string `json:"categories"`
}
