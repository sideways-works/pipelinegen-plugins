// Package models holds the fixture's GORM models, one for each way a
// model can name its table.
package models

import (
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Author takes its table from the naming strategy: authors.
type Author struct {
	ID        uint
	Name      string
	Active    bool
	Notes     []Note
	Profile   Profile
	Languages []Language `gorm:"many2many:author_languages"`
}

// AfterFind runs after every query on authors.
func (a *Author) AfterFind(tx *gorm.DB) error {
	return nil
}

// Note embeds gorm.Model, so deleting one only sets deleted_at.
type Note struct {
	gorm.Model
	AuthorID uint
	Body     string `gorm:"column:text"`
	Tags     []Tag  `gorm:"many2many:note_tags"`
	Comments []Comment
	Meta     Meta   `gorm:"serializer:json"`
	Audit    Audit  `gorm:"embedded;embeddedPrefix:audit_"`
	Scratch  string `gorm:"-"`
}

// BeforeSave runs before every create and update of a note.
func (n *Note) BeforeSave(tx *gorm.DB) error {
	return nil
}

// Meta is stored as JSON in one column.
type Meta struct {
	Pinned bool
}

// Audit is embedded with a prefix.
type Audit struct {
	By string
}

// Tag names its table with a constant.
type Tag struct {
	ID    uint
	Label string
}

// TableName names the table.
func (Tag) TableName() string { return "labels" }

// Comment lives in another schema.
type Comment struct {
	ID     uint
	NoteID uint
}

// TableName names a qualified table.
func (*Comment) TableName() string { return "archive.comments" }

// Profile asks the naming strategy for another name.
type Profile struct {
	ID       uint
	AuthorID uint
}

// TableName applies the strategy to another name.
func (Profile) TableName(namer schema.Namer) string { return namer.TableName("AuthorProfile") }

// Language is on the other side of a many-to-many association.
type Language struct {
	ID   uint
	Code string
}

type stored struct{}

// TableName is promoted to every struct that embeds stored.
func (stored) TableName() string { return "events" }

// Event takes its table from the struct it embeds.
type Event struct {
	stored
	ID uint
}

// Shard's table depends on a field, which GORM never sees: the code
// doesn't say which table it is.
type Shard struct {
	ID     uint
	Region string
}

// TableName is built at run time.
func (s Shard) TableName() string { return "shard_" + s.Region }
