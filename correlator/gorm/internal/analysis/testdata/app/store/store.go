// Package store reaches the fixture's tables in every way GORM allows.
package store

import (
	"context"
	"fmt"
	"time"

	"example.com/app/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store wraps a handle the way repositories do.
type Store struct {
	db *gorm.DB
}

// New stores a handle that comes from outside.
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

const purge = "DELETE FROM drafts WHERE created_at < ?"

// NoteByID reads a note by its primary key.
func (s *Store) NoteByID(id uint) (models.Note, error) {
	var n models.Note
	err := s.db.First(&n, id).Error

	return n, err
}

// CreateNote saves a note with its associations.
func (s *Store) CreateNote(n *models.Note) error {
	return s.db.Create(n).Error
}

// CreateNoteOnly skips the associations.
func (s *Store) CreateNoteOnly(n *models.Note) error {
	return s.db.Omit(clause.Associations).Create(n).Error
}

// Rename updates through a model.
func (s *Store) Rename(id uint, name string) error {
	return s.db.Model(&models.Author{}).Where("id = ?", id).Update("name", name).Error
}

// CountTagged counts in a table no model maps.
func (s *Store) CountTagged(id uint) (int64, error) {
	var n int64
	err := s.db.Table("note_tags").Where("tag_id = ?", id).Count(&n).Error

	return n, err
}

// WithNotes preloads nested associations.
func (s *Store) WithNotes() ([]models.Author, error) {
	var as []models.Author
	err := s.db.Preload("Notes.Tags").Find(&as).Error

	return as, err
}

// WithProfile joins an association.
func (s *Store) WithProfile() ([]models.Author, error) {
	var as []models.Author
	err := s.db.Joins("Profile").Find(&as).Error

	return as, err
}

// Labelled joins with SQL.
func (s *Store) Labelled() ([]models.Note, error) {
	var ns []models.Note
	err := s.db.Joins("JOIN note_tags nt ON nt.note_id = notes.id").Find(&ns).Error

	return ns, err
}

// ByAuthorName filters with a subquery handle.
func (s *Store) ByAuthorName(p string) ([]models.Note, error) {
	var ns []models.Note
	err := s.db.Where("author_id IN (?)", s.db.Table("authors").Select("id").Where("name LIKE ?", p)).Find(&ns).Error

	return ns, err
}

// Tagged filters with a subquery in the condition's text.
func (s *Store) Tagged() ([]models.Note, error) {
	var ns []models.Note
	err := s.db.Where("id IN (SELECT note_id FROM note_tags)").Find(&ns).Error

	return ns, err
}

// Row is what AuthorOf scans into.
type Row struct {
	ID   uint
	Name string
}

// AuthorOf runs raw SQL.
func (s *Store) AuthorOf(id uint) (Row, error) {
	var r Row
	err := s.db.Raw(`SELECT a.id, a.name FROM authors a JOIN notes n ON n.author_id = a.id WHERE n.id = ?`, id).Scan(&r).Error

	return r, err
}

// Purge runs SQL from a constant.
func (s *Store) Purge(before time.Time) error {
	return s.db.Exec(purge, before).Error
}

// Blank runs SQL from a variable assigned once.
func (s *Store) Blank(id uint) error {
	q := "UPDATE notes SET text = '' WHERE id = ?"

	return s.db.Exec(q, id).Error
}

// Drop builds its SQL at run time.
func (s *Store) Drop(table string) error {
	return s.db.Exec(fmt.Sprintf("DELETE FROM %s", table)).Error
}

// Size names its table at run time.
func (s *Store) Size(table string) (int64, error) {
	var n int64
	err := s.db.Table(table).Count(&n).Error

	return n, err
}

// Save takes any model.
func (s *Store) Save(v any) error {
	return s.db.Save(v).Error
}

// Total counts whatever table its caller chose.
func Total(q *gorm.DB) int64 {
	var n int64
	q.Count(&n)

	return n
}

// Speak adds languages to an author.
func (s *Store) Speak(a *models.Author, ls []models.Language) error {
	return s.db.Model(a).Association("Languages").Append(ls)
}

// FirstNote uses the generic API.
func (s *Store) FirstNote(ctx context.Context, id uint) (models.Note, error) {
	return gorm.G[models.Note](s.db).Where("id = ?", id).First(ctx)
}

// AddTag creates through the generic API.
func (s *Store) AddTag(ctx context.Context, t *models.Tag) error {
	return gorm.G[models.Tag](s.db).Create(ctx, t)
}

// AddLanguage writes in a transaction.
func (s *Store) AddLanguage(code string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&models.Language{Code: code}).Error
	})
}

func archived(db *gorm.DB) *gorm.DB {
	return db.Table("archive.old_comments")
}

// Archived reads through a scope that sets the table.
func (s *Store) Archived() ([]models.Comment, error) {
	var cs []models.Comment
	err := s.db.Scopes(archived).Find(&cs).Error

	return cs, err
}

func (s *Store) authors() *gorm.DB {
	return s.db.Model(&models.Author{})
}

// Deactivate updates through a helper that picks the model.
func (s *Store) Deactivate() error {
	return s.authors().Where("active").Update("active", false).Error
}

// Shards reads a model whose table is decided at run time.
func (s *Store) Shards() ([]models.Shard, error) {
	var ss []models.Shard
	err := s.db.Find(&ss).Error

	return ss, err
}

// Record writes a model whose table comes from an embedded struct.
func (s *Store) Record() error {
	return s.db.Create(&models.Event{}).Error
}

// FirstLabel writes an expression with a subquery.
func (s *Store) FirstLabel(id uint) error {
	return s.db.Model(&models.Note{}).Where("id = ?", id).Update("text", gorm.Expr("(SELECT label FROM labels LIMIT 1)")).Error
}

// Forget deletes an author with its associations.
func (s *Store) Forget(a *models.Author) error {
	return s.db.Select(clause.Associations).Delete(a).Error
}

// Register adds a callback GORM runs before every create.
func Register(db *gorm.DB) error {
	return db.Callback().Create().Before("gorm:create").Register("app:stamp", stamp)
}

func stamp(db *gorm.DB) {}

// Score calls a function the schema defines, and built-ins.
func (s *Store) Score(id uint) (float64, error) {
	var v float64
	err := s.db.Raw(`SELECT note_score(id) + count(*) FROM notes WHERE id = ? AND created_at < now() GROUP BY id`, id).Scan(&v).Error

	return v, err
}

// Locked reads with clauses: a lock names no other table, an expression
// and a table literal do.
func (s *Store) Locked(id uint) ([]models.Note, error) {
	var ns []models.Note
	err := s.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Clauses(clause.Expr{SQL: "id IN (SELECT note_id FROM note_tags)"}).
		Clauses(clause.From{Tables: []clause.Table{{Name: "drafts"}}}).
		Find(&ns, id).Error

	return ns, err
}

// Clause takes a clause built elsewhere.
func (s *Store) Clause(e clause.Expression) ([]models.Note, error) {
	var ns []models.Note
	err := s.db.Clauses(e).Find(&ns).Error

	return ns, err
}
